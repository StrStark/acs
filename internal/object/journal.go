package object

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// The metadata journal makes the storage backends self-describing: every
// committed Pebble batch is handed to a journal consumer (the replicator),
// which copies it to all backends. Together with periodic snapshots this lets
// a new machine rebuild the whole object index from any backend.
//
// Sequence numbers are time-based (microseconds, strictly increasing), so they
// keep increasing across restarts without being persisted. Replaying records
// that are already in a snapshot is harmless: sets and deletes converge, and
// stats (the only merge operands) are recomputed after every restore.

// Record is one committed metadata batch.
type Record struct {
	Seq   uint64
	Batch []byte
}

type inflightSet struct {
	mu  sync.Mutex
	set map[uint64]struct{}
}

func (s *Service) beginSeq() uint64 {
	// Allocation and registration happen under one lock so SafeSeq never
	// sees a number that is allocated but not yet in flight.
	s.inflight.mu.Lock()
	defer s.inflight.mu.Unlock()
	seq := uint64(time.Now().UnixMicro())
	if last := s.jseq.Load(); seq <= last {
		seq = last + 1
	}
	s.jseq.Store(seq)
	if s.inflight.set == nil {
		s.inflight.set = map[uint64]struct{}{}
	}
	s.inflight.set[seq] = struct{}{}
	return seq
}

func (s *Service) endSeq(seq uint64) {
	s.inflight.mu.Lock()
	delete(s.inflight.set, seq)
	s.inflight.mu.Unlock()
}

// SafeSeq returns a sequence number such that every batch with a lower or
// equal number has either committed and been journaled, or failed.
func (s *Service) SafeSeq() uint64 {
	s.inflight.mu.Lock()
	defer s.inflight.mu.Unlock()
	safe := s.jseq.Load()
	for seq := range s.inflight.set {
		if seq-1 < safe {
			safe = seq - 1
		}
	}
	return safe
}

// SetJournal installs the consumer of committed batches. It must not block.
func (s *Service) SetJournal(fn func(Record)) { s.journal = fn }

func (s *Service) journalBatch(seq uint64, b *pebble.Batch) {
	if s.journal == nil || b.Empty() {
		return
	}
	s.journal(Record{Seq: seq, Batch: bytes.Clone(b.Repr())})
}

// ---- snapshots ----

var (
	snapMagic    = []byte("ACSSNAP1\n")
	journalMagic = []byte("ACSJRNL1\n")
)

// ExportSnapshot writes the complete object index to w (gzip) and returns the
// sequence number it covers: replaying records with a higher number on top of
// it reproduces the current state.
func (s *Service) ExportSnapshot(w io.Writer) (uint64, error) {
	from := s.SafeSeq()
	snap := s.db.NewSnapshot()
	defer snap.Close()
	it, err := snap.NewIter(nil)
	if err != nil {
		return 0, err
	}
	defer it.Close()

	zw := gzip.NewWriter(w)
	bw := bufio.NewWriterSize(zw, 1<<20)
	bw.Write(snapMagic)
	writeUvarint(bw, from)
	for it.First(); it.Valid(); it.Next() {
		val, err := it.ValueAndErr()
		if err != nil {
			return 0, err
		}
		writeUvarint(bw, uint64(len(it.Key())))
		bw.Write(it.Key())
		writeUvarint(bw, uint64(len(val)))
		bw.Write(val)
	}
	if err := it.Error(); err != nil {
		return 0, err
	}
	writeUvarint(bw, 0) // end marker (keys are never empty)
	if err := bw.Flush(); err != nil {
		return 0, err
	}
	return from, zw.Close()
}

func writeUvarint(w io.Writer, v uint64) {
	var buf [binary.MaxVarintLen64]byte
	w.Write(buf[:binary.PutUvarint(buf[:], v)])
}

// readSnapshot streams key/value pairs from a snapshot.
func readSnapshot(r io.Reader, fn func(k, v []byte) error) (uint64, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return 0, fmt.Errorf("snapshot: %w", err)
	}
	br := bufio.NewReaderSize(zr, 1<<20)
	magic := make([]byte, len(snapMagic))
	if _, err := io.ReadFull(br, magic); err != nil || !bytes.Equal(magic, snapMagic) {
		return 0, errors.New("snapshot: bad header")
	}
	from, err := binary.ReadUvarint(br)
	if err != nil {
		return 0, err
	}
	for {
		kl, err := binary.ReadUvarint(br)
		if err != nil {
			return 0, fmt.Errorf("snapshot truncated: %w", err)
		}
		if kl == 0 {
			return from, nil
		}
		k := make([]byte, kl)
		if _, err := io.ReadFull(br, k); err != nil {
			return 0, err
		}
		vl, err := binary.ReadUvarint(br)
		if err != nil {
			return 0, err
		}
		v := make([]byte, vl)
		if _, err := io.ReadFull(br, v); err != nil {
			return 0, err
		}
		if err := fn(k, v); err != nil {
			return 0, err
		}
	}
}

// ---- journal segments ----

// EncodeSegment serialises records as a journal segment.
func EncodeSegment(recs []Record) []byte {
	var buf bytes.Buffer
	buf.Write(journalMagic)
	for _, r := range recs {
		var hdr [8]byte
		binary.BigEndian.PutUint64(hdr[:], r.Seq)
		writeUvarint(&buf, r.Seq)
		writeUvarint(&buf, uint64(len(r.Batch)))
		buf.Write(r.Batch)
		crc := crc32.NewIEEE()
		crc.Write(hdr[:])
		crc.Write(r.Batch)
		binary.Write(&buf, binary.BigEndian, crc.Sum32())
	}
	return buf.Bytes()
}

// DecodeSegment parses a journal segment, stopping at the first damaged
// record (a torn write at the end of a segment loses only that tail).
func DecodeSegment(data []byte) ([]Record, error) {
	if !bytes.HasPrefix(data, journalMagic) {
		return nil, errors.New("journal: bad header")
	}
	r := bytes.NewReader(data[len(journalMagic):])
	var out []Record
	for r.Len() > 0 {
		seq, err := binary.ReadUvarint(r)
		if err != nil {
			break
		}
		n, err := binary.ReadUvarint(r)
		if err != nil || n > uint64(r.Len()) {
			break
		}
		batch := make([]byte, n)
		io.ReadFull(r, batch)
		var sum uint32
		if binary.Read(r, binary.BigEndian, &sum) != nil {
			break
		}
		var hdr [8]byte
		binary.BigEndian.PutUint64(hdr[:], seq)
		crc := crc32.NewIEEE()
		crc.Write(hdr[:])
		crc.Write(batch)
		if crc.Sum32() != sum {
			break
		}
		out = append(out, Record{Seq: seq, Batch: batch})
	}
	return out, nil
}

// ---- restore ----

// RestoreState builds a fresh object index in metaDir from a snapshot (may be
// nil) and journal records, then recomputes bucket stats. metaDir must not
// contain a database.
func RestoreState(metaDir string, snapshot io.Reader, records []Record) (uint64, error) {
	if entries, _ := os.ReadDir(metaDir); len(entries) > 0 {
		return 0, fmt.Errorf("restore target %s is not empty", metaDir)
	}
	db, err := pebble.Open(metaDir, &pebble.Options{Merger: statsMerger, Logger: pebbleLogger{}})
	if err != nil {
		return 0, err
	}
	defer db.Close()
	last, err := loadState(db, snapshot, records)
	if err != nil {
		return 0, err
	}
	// New sequence numbers on this machine must follow the restored ones,
	// even if its clock is behind.
	var floor [8]byte
	binary.BigEndian.PutUint64(floor[:], last)
	if err := db.Set(seqFloorKey, floor[:], pebble.Sync); err != nil {
		return 0, err
	}
	return last, recomputeAllStats(db)
}

var seqFloorKey = k("M", "seqfloor")

// loadSeqFloor raises the sequence counter to the floor left by a restore.
func (s *Service) loadSeqFloor() {
	val, closer, err := s.db.Get(seqFloorKey)
	if err != nil {
		return
	}
	defer closer.Close()
	if len(val) == 8 {
		s.jseq.Store(binary.BigEndian.Uint64(val))
	}
}

func loadState(db *pebble.DB, snapshot io.Reader, records []Record) (uint64, error) {
	var from uint64
	if snapshot != nil {
		batch := db.NewBatch()
		n := 0
		var err error
		from, err = readSnapshot(snapshot, func(k, v []byte) error {
			batch.Set(k, v, nil)
			if n++; n%5000 == 0 {
				if err := batch.Commit(pebble.NoSync); err != nil {
					return err
				}
				batch.Close()
				batch = db.NewBatch()
			}
			return nil
		})
		if err == nil {
			err = batch.Commit(pebble.NoSync)
		}
		batch.Close()
		if err != nil {
			return 0, err
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Seq < records[j].Seq })
	last := from
	for _, r := range records {
		if r.Seq <= from {
			continue
		}
		b := db.NewBatch()
		if err := b.SetRepr(r.Batch); err != nil {
			b.Close()
			return 0, fmt.Errorf("journal record %d: %w", r.Seq, err)
		}
		if err := b.Commit(pebble.NoSync); err != nil {
			b.Close()
			return 0, err
		}
		b.Close()
		last = r.Seq
	}
	return last, db.Flush()
}

// recomputeAllStats rebuilds every bucket's stats from its records.
func recomputeAllStats(db *pebble.DB) error {
	stats := map[string]*Stats{}
	get := func(b string) *Stats {
		if stats[b] == nil {
			stats[b] = &Stats{}
		}
		return stats[b]
	}
	it, err := db.NewIter(&pebble.IterOptions{LowerBound: k("B", ""), UpperBound: prefixEnd(k("B", ""))})
	if err != nil {
		return err
	}
	for it.First(); it.Valid(); it.Next() {
		get(string(it.Key()[2:]))
	}
	it.Close()
	for _, p := range [][]byte{k("O", ""), k("V", "")} {
		it, err := db.NewIter(&pebble.IterOptions{LowerBound: p, UpperBound: prefixEnd(p)})
		if err != nil {
			return err
		}
		for it.First(); it.Valid(); it.Next() {
			rest := it.Key()[2:]
			bucket := string(rest[:bytes.IndexByte(rest, 0)])
			var o Object
			if err := jsonUnmarshal(it.Value(), &o); err != nil {
				it.Close()
				return err
			}
			st := get(bucket)
			if p[0] == 'O' {
				st.Objects++
				st.Bytes += o.Size
			} else {
				st.StoredBytes += o.Size
			}
		}
		it.Close()
	}
	batch := db.NewBatch()
	defer batch.Close()
	sp := k("S", "")
	if err := batch.DeleteRange(sp, prefixEnd(sp), nil); err != nil {
		return err
	}
	for b, st := range stats {
		batch.Set(statsKey(b), st.encode(), nil)
	}
	return batch.Commit(pebble.Sync)
}

// RecomputeStats rebuilds all bucket stats of the live index.
func (s *Service) RecomputeStats() error { return recomputeAllStats(s.db) }

// ImportResult reports an ImportBuckets call.
type ImportResult struct {
	Imported []string `json:"imported"`
	Skipped  []string `json:"skipped"`
}

// ImportBuckets copies buckets from another installation's metadata (a
// snapshot plus journal records) into this index. Only buckets whose data
// lives on backend are taken; buckets whose name already exists are skipped.
func (s *Service) ImportBuckets(ctx context.Context, snapshot io.Reader, records []Record, backend string) (*ImportResult, error) {
	// Next to the metadata directory: the container image has no /tmp.
	tmp, err := os.MkdirTemp(filepath.Dir(s.metaDir), ".import-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	foreign, err := pebble.Open(tmp, &pebble.Options{Merger: statsMerger, Logger: pebbleLogger{}})
	if err != nil {
		return nil, err
	}
	defer foreign.Close()
	if _, err := loadState(foreign, snapshot, records); err != nil {
		return nil, err
	}

	res := &ImportResult{Imported: []string{}, Skipped: []string{}}
	var buckets []Bucket
	it, err := foreign.NewIter(&pebble.IterOptions{LowerBound: bucketPrefix(), UpperBound: prefixEnd(bucketPrefix())})
	if err != nil {
		return nil, err
	}
	for it.First(); it.Valid(); it.Next() {
		var b Bucket
		if jsonUnmarshal(it.Value(), &b) == nil && b.Backend == backend {
			buckets = append(buckets, b)
		}
	}
	it.Close()

	s.bucketMu.Lock()
	defer s.bucketMu.Unlock()
	for _, b := range buckets {
		if _, err := s.GetBucket(ctx, b.Name); err == nil {
			res.Skipped = append(res.Skipped, b.Name)
			continue
		}
		var uploadIDs []string
		batch := s.db.NewBatch()
		copyPrefix := func(p []byte) error {
			it, err := foreign.NewIter(&pebble.IterOptions{LowerBound: p, UpperBound: prefixEnd(p)})
			if err != nil {
				return err
			}
			defer it.Close()
			for it.First(); it.Valid(); it.Next() {
				batch.Set(bytes.Clone(it.Key()), bytes.Clone(it.Value()), nil)
				if p[0] == 'U' {
					var u Upload
					if jsonUnmarshal(it.Value(), &u) == nil {
						uploadIDs = append(uploadIDs, u.ID)
					}
				}
			}
			return it.Error()
		}
		for _, p := range [][]byte{bucketKey(b.Name), objPrefix(b.Name), verPrefix(b.Name), uploadPrefix(b.Name)} {
			if err := copyPrefix(p); err != nil {
				batch.Close()
				return nil, err
			}
		}
		for _, id := range uploadIDs {
			if err := copyPrefix(partPrefix(id)); err != nil {
				batch.Close()
				return nil, err
			}
		}
		if err := s.commit(batch); err != nil {
			return nil, err
		}
		res.Imported = append(res.Imported, b.Name)
	}
	if len(res.Imported) > 0 {
		if err := s.RecomputeStats(); err != nil {
			return nil, err
		}
	}
	return res, nil
}

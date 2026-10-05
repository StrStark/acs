package object

import "fmt"

// Key layout in Pebble. Components are separated by 0x00, which is rejected in
// bucket names and object keys, so byte order matches S3's UTF-8 key order.
//
//	B\0<bucket>                         bucket record
//	S\0<bucket>                         bucket stats (merge operator)
//	O\0<bucket>\0<key>                  current version of an object (copy of its V record)
//	V\0<bucket>\0<key>\0<seq>           every version, newest first (seq is descending)
//	U\0<bucket>\0<key>\0<uploadID>      in-progress multipart upload
//	P\0<uploadID>\0<part:05d>           uploaded part
//	G\0<blobID>                         blob pending deletion (value: enqueue time)

func k(parts ...string) []byte {
	n := 0
	for _, p := range parts {
		n += len(p) + 1
	}
	b := make([]byte, 0, n)
	for i, p := range parts {
		if i > 0 {
			b = append(b, 0)
		}
		b = append(b, p...)
	}
	return b
}

func bucketKey(b string) []byte         { return k("B", b) }
func bucketPrefix() []byte              { return k("B", "") }
func statsKey(b string) []byte          { return k("S", b) }
func objPrefix(b string) []byte         { return k("O", b, "") }
func objKey(b, key string) []byte       { return k("O", b, key) }
func verPrefix(b string) []byte         { return k("V", b, "") }
func verKeyPrefix(b, key string) []byte { return k("V", b, key, "") }
func verKey(b, key, seq string) []byte  { return k("V", b, key, seq) }
func uploadPrefix(b string) []byte      { return k("U", b, "") }
func uploadKey(b, key, id string) []byte {
	return k("U", b, key, id)
}
func partPrefix(uploadID string) []byte { return k("P", uploadID, "") }
func partKey(uploadID string, n int) []byte {
	return k("P", uploadID, fmt.Sprintf("%05d", n))
}
func gcKey(blobID string) []byte { return k("G", blobID) }
func gcPrefix() []byte           { return k("G", "") }

// prefixEnd returns the smallest key greater than every key with prefix p.
func prefixEnd(p []byte) []byte {
	end := append([]byte(nil), p...)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] < 0xff {
			end[i]++
			return end[:i+1]
		}
	}
	return nil
}

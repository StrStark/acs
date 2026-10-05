package s3

import (
	"encoding/xml"
	"time"
)

const xmlns = "http://s3.amazonaws.com/doc/2006-03-01/"

// s3Time formats timestamps the way S3 does in XML bodies.
func s3Time(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

type owner struct {
	ID          string `xml:"ID"`
	DisplayName string `xml:"DisplayName"`
}

var defaultOwner = owner{ID: "acs", DisplayName: "acs"}

type errorResponse struct {
	XMLName    xml.Name `xml:"Error"`
	Code       string   `xml:"Code"`
	Message    string   `xml:"Message"`
	BucketName string   `xml:"BucketName,omitempty"`
	Key        string   `xml:"Key,omitempty"`
	Resource   string   `xml:"Resource,omitempty"`
	RequestID  string   `xml:"RequestId"`
}

type listAllMyBucketsResult struct {
	XMLName xml.Name `xml:"ListAllMyBucketsResult"`
	Xmlns   string   `xml:"xmlns,attr"`
	Owner   owner    `xml:"Owner"`
	Buckets struct {
		Bucket []bucketEntry `xml:"Bucket"`
	} `xml:"Buckets"`
}

type bucketEntry struct {
	Name         string `xml:"Name"`
	CreationDate string `xml:"CreationDate"`
}

type contentEntry struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
	StorageClass string `xml:"StorageClass"`
	Owner        *owner `xml:"Owner,omitempty"`
}

type commonPrefix struct {
	Prefix string `xml:"Prefix"`
}

type listBucketResult struct {
	XMLName        xml.Name       `xml:"ListBucketResult"`
	Xmlns          string         `xml:"xmlns,attr"`
	Name           string         `xml:"Name"`
	Prefix         string         `xml:"Prefix"`
	Marker         string         `xml:"Marker"`
	NextMarker     string         `xml:"NextMarker,omitempty"`
	MaxKeys        int            `xml:"MaxKeys"`
	Delimiter      string         `xml:"Delimiter,omitempty"`
	IsTruncated    bool           `xml:"IsTruncated"`
	EncodingType   string         `xml:"EncodingType,omitempty"`
	Contents       []contentEntry `xml:"Contents"`
	CommonPrefixes []commonPrefix `xml:"CommonPrefixes"`
}

type listBucketResultV2 struct {
	XMLName               xml.Name       `xml:"ListBucketResult"`
	Xmlns                 string         `xml:"xmlns,attr"`
	Name                  string         `xml:"Name"`
	Prefix                string         `xml:"Prefix"`
	StartAfter            string         `xml:"StartAfter,omitempty"`
	ContinuationToken     string         `xml:"ContinuationToken,omitempty"`
	NextContinuationToken string         `xml:"NextContinuationToken,omitempty"`
	KeyCount              int            `xml:"KeyCount"`
	MaxKeys               int            `xml:"MaxKeys"`
	Delimiter             string         `xml:"Delimiter,omitempty"`
	IsTruncated           bool           `xml:"IsTruncated"`
	EncodingType          string         `xml:"EncodingType,omitempty"`
	Contents              []contentEntry `xml:"Contents"`
	CommonPrefixes        []commonPrefix `xml:"CommonPrefixes"`
}

type versionEntry struct {
	XMLName      xml.Name
	Key          string `xml:"Key"`
	VersionID    string `xml:"VersionId"`
	IsLatest     bool   `xml:"IsLatest"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag,omitempty"`
	Size         *int64 `xml:"Size,omitempty"`
	StorageClass string `xml:"StorageClass,omitempty"`
	Owner        owner  `xml:"Owner"`
}

type listVersionsResult struct {
	XMLName             xml.Name `xml:"ListVersionsResult"`
	Xmlns               string   `xml:"xmlns,attr"`
	Name                string   `xml:"Name"`
	Prefix              string   `xml:"Prefix"`
	KeyMarker           string   `xml:"KeyMarker"`
	VersionIDMarker     string   `xml:"VersionIdMarker"`
	NextKeyMarker       string   `xml:"NextKeyMarker,omitempty"`
	NextVersionIDMarker string   `xml:"NextVersionIdMarker,omitempty"`
	MaxKeys             int      `xml:"MaxKeys"`
	Delimiter           string   `xml:"Delimiter,omitempty"`
	IsTruncated         bool     `xml:"IsTruncated"`
	EncodingType        string   `xml:"EncodingType,omitempty"`
	// Entries holds Version and DeleteMarker elements (named by XMLName).
	Entries        []versionEntry `xml:"Version"`
	CommonPrefixes []commonPrefix `xml:"CommonPrefixes"`
}

type versioningConfiguration struct {
	XMLName xml.Name `xml:"VersioningConfiguration"`
	Xmlns   string   `xml:"xmlns,attr,omitempty"`
	Status  string   `xml:"Status,omitempty"`
}

type locationConstraint struct {
	XMLName xml.Name `xml:"LocationConstraint"`
	Xmlns   string   `xml:"xmlns,attr"`
	Value   string   `xml:",chardata"`
}

type corsConfiguration struct {
	XMLName xml.Name `xml:"CORSConfiguration"`
	Xmlns   string   `xml:"xmlns,attr,omitempty"`
	Rules   []struct {
		AllowedOrigin []string `xml:"AllowedOrigin"`
		AllowedMethod []string `xml:"AllowedMethod"`
		AllowedHeader []string `xml:"AllowedHeader"`
		ExposeHeader  []string `xml:"ExposeHeader"`
		MaxAgeSeconds int      `xml:"MaxAgeSeconds,omitempty"`
	} `xml:"CORSRule"`
}

type lifecycleRule struct {
	ID     string `xml:"ID,omitempty"`
	Status string `xml:"Status"`
	Prefix string `xml:"Prefix,omitempty"`
	Filter *struct {
		Prefix string `xml:"Prefix"`
	} `xml:"Filter,omitempty"`
	Expiration *struct {
		Days int `xml:"Days,omitempty"`
	} `xml:"Expiration,omitempty"`
	NoncurrentVersionExpiration *struct {
		NoncurrentDays int `xml:"NoncurrentDays,omitempty"`
	} `xml:"NoncurrentVersionExpiration,omitempty"`
	AbortIncompleteMultipartUpload *struct {
		DaysAfterInitiation int `xml:"DaysAfterInitiation,omitempty"`
	} `xml:"AbortIncompleteMultipartUpload,omitempty"`
}

type lifecycleConfiguration struct {
	XMLName xml.Name        `xml:"LifecycleConfiguration"`
	Xmlns   string          `xml:"xmlns,attr,omitempty"`
	Rules   []lifecycleRule `xml:"Rule"`
}

type tag struct {
	Key   string `xml:"Key"`
	Value string `xml:"Value"`
}

type tagging struct {
	XMLName xml.Name `xml:"Tagging"`
	Xmlns   string   `xml:"xmlns,attr,omitempty"`
	TagSet  struct {
		Tags []tag `xml:"Tag"`
	} `xml:"TagSet"`
}

type grant struct {
	Grantee struct {
		XMLNS       string `xml:"xmlns:xsi,attr"`
		Type        string `xml:"xsi:type,attr"`
		ID          string `xml:"ID"`
		DisplayName string `xml:"DisplayName"`
	} `xml:"Grantee"`
	Permission string `xml:"Permission"`
}

type accessControlPolicy struct {
	XMLName           xml.Name `xml:"AccessControlPolicy"`
	Xmlns             string   `xml:"xmlns,attr"`
	Owner             owner    `xml:"Owner"`
	AccessControlList struct {
		Grant []grant `xml:"Grant"`
	} `xml:"AccessControlList"`
}

func fullControlACL() accessControlPolicy {
	var g grant
	g.Grantee.XMLNS = "http://www.w3.org/2001/XMLSchema-instance"
	g.Grantee.Type = "CanonicalUser"
	g.Grantee.ID = defaultOwner.ID
	g.Grantee.DisplayName = defaultOwner.DisplayName
	g.Permission = "FULL_CONTROL"
	acl := accessControlPolicy{Xmlns: xmlns, Owner: defaultOwner}
	acl.AccessControlList.Grant = []grant{g}
	return acl
}

type deleteRequest struct {
	Quiet   bool `xml:"Quiet"`
	Objects []struct {
		Key       string `xml:"Key"`
		VersionID string `xml:"VersionId"`
	} `xml:"Object"`
}

type deletedEntry struct {
	Key                   string `xml:"Key"`
	VersionID             string `xml:"VersionId,omitempty"`
	DeleteMarker          bool   `xml:"DeleteMarker,omitempty"`
	DeleteMarkerVersionID string `xml:"DeleteMarkerVersionId,omitempty"`
}

type deleteErrorEntry struct {
	Key       string `xml:"Key"`
	VersionID string `xml:"VersionId,omitempty"`
	Code      string `xml:"Code"`
	Message   string `xml:"Message"`
}

type deleteResult struct {
	XMLName xml.Name           `xml:"DeleteResult"`
	Xmlns   string             `xml:"xmlns,attr"`
	Deleted []deletedEntry     `xml:"Deleted"`
	Errors  []deleteErrorEntry `xml:"Error"`
}

type copyObjectResult struct {
	XMLName      xml.Name `xml:"CopyObjectResult"`
	Xmlns        string   `xml:"xmlns,attr"`
	LastModified string   `xml:"LastModified"`
	ETag         string   `xml:"ETag"`
}

type copyPartResult struct {
	XMLName      xml.Name `xml:"CopyPartResult"`
	Xmlns        string   `xml:"xmlns,attr"`
	LastModified string   `xml:"LastModified"`
	ETag         string   `xml:"ETag"`
}

type initiateMultipartUploadResult struct {
	XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
	Xmlns    string   `xml:"xmlns,attr"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	UploadID string   `xml:"UploadId"`
}

type completeMultipartUpload struct {
	Parts []struct {
		PartNumber int    `xml:"PartNumber"`
		ETag       string `xml:"ETag"`
	} `xml:"Part"`
}

type completeMultipartUploadResult struct {
	XMLName  xml.Name `xml:"CompleteMultipartUploadResult"`
	Xmlns    string   `xml:"xmlns,attr"`
	Location string   `xml:"Location"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	ETag     string   `xml:"ETag"`
}

type partEntry struct {
	PartNumber   int    `xml:"PartNumber"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
}

type listPartsResult struct {
	XMLName              xml.Name    `xml:"ListPartsResult"`
	Xmlns                string      `xml:"xmlns,attr"`
	Bucket               string      `xml:"Bucket"`
	Key                  string      `xml:"Key"`
	UploadID             string      `xml:"UploadId"`
	Initiator            owner       `xml:"Initiator"`
	Owner                owner       `xml:"Owner"`
	StorageClass         string      `xml:"StorageClass"`
	PartNumberMarker     int         `xml:"PartNumberMarker"`
	NextPartNumberMarker int         `xml:"NextPartNumberMarker"`
	MaxParts             int         `xml:"MaxParts"`
	IsTruncated          bool        `xml:"IsTruncated"`
	Parts                []partEntry `xml:"Part"`
}

type uploadEntry struct {
	Key          string `xml:"Key"`
	UploadID     string `xml:"UploadId"`
	Initiator    owner  `xml:"Initiator"`
	Owner        owner  `xml:"Owner"`
	StorageClass string `xml:"StorageClass"`
	Initiated    string `xml:"Initiated"`
}

type listMultipartUploadsResult struct {
	XMLName            xml.Name      `xml:"ListMultipartUploadsResult"`
	Xmlns              string        `xml:"xmlns,attr"`
	Bucket             string        `xml:"Bucket"`
	KeyMarker          string        `xml:"KeyMarker"`
	UploadIDMarker     string        `xml:"UploadIdMarker"`
	NextKeyMarker      string        `xml:"NextKeyMarker,omitempty"`
	NextUploadIDMarker string        `xml:"NextUploadIdMarker,omitempty"`
	Prefix             string        `xml:"Prefix"`
	MaxUploads         int           `xml:"MaxUploads"`
	IsTruncated        bool          `xml:"IsTruncated"`
	Uploads            []uploadEntry `xml:"Upload"`
}

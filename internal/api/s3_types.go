// Package api hosts the external HTTP surface of shardstore.
package api

import (
	"encoding/xml"
	"time"
)

// S3ErrorResponse represents an S3-style error response in XML.
type S3ErrorResponse struct {
	XMLName    xml.Name `xml:"Error"`
	Code       string   `xml:"Code"`
	Message    string   `xml:"Message"`
	Resource   string   `xml:"Resource"`
	RequestID  string   `xml:"RequestId"`
	HostID     string   `xml:"HostId"`
	BucketName string   `xml:"BucketName,omitempty"`
	ObjectName string   `xml:"Key,omitempty"`
}

// S3Error codes (subset of S3 error codes)
const (
	ErrCodeInvalidRequest         = "InvalidRequest"
	ErrCodeNoSuchBucket           = "NoSuchBucket"
	ErrCodeBucketAlreadyExists    = "BucketAlreadyExists"
	ErrCodeBucketAlreadyOwnedByYou = "BucketAlreadyOwnedByYou"
	ErrCodeNoSuchKey              = "NoSuchKey"
	ErrCodeInvalidObjectState     = "InvalidObjectState"
	ErrCodeMethodNotAllowed       = "MethodNotAllowed"
	ErrCodeInvalidArgument        = "InvalidArgument"
	ErrCodeInternalError          = "InternalError"
	ErrCodeNotImplemented         = "NotImplemented"
	ErrCodeAccessDenied           = "AccessDenied"
	ErrCodeSignatureDoesNotMatch  = "SignatureDoesNotMatch"
	ErrCodeInvalidAccessKeyId     = "InvalidAccessKeyId"
	ErrCodeMissingContentLength   = "MissingContentLength"
	ErrCodeEntityTooSmall         = "EntityTooSmall"
	ErrCodeEntityTooLarge         = "EntityTooLarge"
	ErrCodeBadDigest              = "BadDigest"
	ErrCodeInvalidRange           = "InvalidRange"
	ErrCodePreconditionFailed     = "PreconditionFailed"
)

// Bucket represents an S3 bucket.
type Bucket struct {
	Name         string    `xml:"Name"`
	CreationDate time.Time `xml:"CreationDate"`
}

// ListBucketsResult is the response for ListBuckets.
type ListBucketsResult struct {
	XMLName xml.Name `xml:"ListAllMyBucketsResult"`
	Owner   Owner    `xml:"Owner"`
	Buckets []Bucket `xml:"Buckets>Bucket"`
}

// Owner represents the bucket/object owner.
type Owner struct {
	ID          string `xml:"ID"`
	DisplayName string `xml:"DisplayName"`
}

// Object represents an S3 object in a ListObjectsV2 response.
type Object struct {
	Key          string    `xml:"Key"`
	LastModified time.Time `xml:"LastModified"`
	ETag         string    `xml:"ETag"`
	Size         int64     `xml:"Size"`
	StorageClass string    `xml:"StorageClass"`
	Owner        Owner     `xml:"Owner"`
}

// CommonPrefix represents a common prefix in ListObjectsV2 (for delimiter).
type CommonPrefix struct {
	Prefix string `xml:"Prefix"`
}

// ListObjectsV2Result is the response for ListObjectsV2.
type ListObjectsV2Result struct {
	XMLName        xml.Name       `xml:"ListBucketResult"`
	Name           string         `xml:"Name"`
	Prefix         string         `xml:"Prefix"`
	KeyCount       int            `xml:"KeyCount"`
	MaxKeys        int            `xml:"MaxKeys"`
	Delimiter      string         `xml:"Delimiter"`
	IsTruncated    bool           `xml:"IsTruncated"`
	NextContinuationToken string  `xml:"NextContinuationToken,omitempty"`
	Contents       []Object       `xml:"Contents"`
	CommonPrefixes []CommonPrefix `xml:"CommonPrefixes"`
}

// CreateMultipartUploadResult is the response for CreateMultipartUpload.
type CreateMultipartUploadResult struct {
	XMLName xml.Name `xml:"InitiateMultipartUploadResult"`
	Bucket  string   `xml:"Bucket"`
	Key     string   `xml:"Key"`
	UploadID string  `xml:"UploadId"`
}

// UploadPartResult is the response for UploadPart.
type UploadPartResult struct {
	XMLName xml.Name `xml:"CopyPartResult"`
	PartNumber int   `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
}

// CompleteMultipartUploadResult is the response for CompleteMultipartUpload.
type CompleteMultipartUploadResult struct {
	XMLName xml.Name `xml:"CompleteMultipartUploadResult"`
	Location string  `xml:"Location"`
	Bucket   string  `xml:"Bucket"`
	Key      string  `xml:"Key"`
	ETag     string  `xml:"ETag"`
}

// ListPartsResult is the response for ListParts.
type ListPartsResult struct {
	XMLName        xml.Name     `xml:"ListPartsResult"`
	Bucket         string       `xml:"Bucket"`
	Key            string       `xml:"Key"`
	UploadID       string       `xml:"UploadId"`
	Initiator      Initiator    `xml:"Initiator"`
	Owner          Owner        `xml:"Owner"`
	StorageClass   string       `xml:"StorageClass"`
	PartNumberMarker int        `xml:"PartNumberMarker"`
	NextPartNumberMarker int    `xml:"NextPartNumberMarker"`
	MaxParts       int          `xml:"MaxParts"`
	IsTruncated    bool         `xml:"IsTruncated"`
	Parts          []Part       `xml:"Part"`
}

// Initiator represents the multipart upload initiator.
type Initiator struct {
	ID          string `xml:"ID"`
	DisplayName string `xml:"DisplayName"`
}

// Part represents a part in a multipart upload.
type Part struct {
	PartNumber   int       `xml:"PartNumber"`
	LastModified time.Time `xml:"LastModified"`
	ETag         string    `xml:"ETag"`
	Size         int64     `xml:"Size"`
}

// DeleteResult is the response for DeleteObjects.
type DeleteResult struct {
	XMLName   xml.Name    `xml:"DeleteResult"`
	Deleted   []DeletedObject `xml:"Deleted"`
	Errors    []DeleteError   `xml:"Error"`
}

// DeletedObject represents a successfully deleted object.
type DeletedObject struct {
	Key       string `xml:"Key"`
	VersionId string `xml:"VersionId,omitempty"`
	DeleteMarker bool  `xml:"DeleteMarker,omitempty"`
	DeleteMarkerVersionId string `xml:"DeleteMarkerVersionId,omitempty"`
}

// DeleteError represents an error deleting an object.
type DeleteError struct {
	Key     string `xml:"Key"`
	VersionId string `xml:"VersionId,omitempty"`
	Code    string `xml:"Code"`
	Message string `xml:"Message"`
}

// Delete represents the request body for DeleteObjects.
type Delete struct {
	XMLName xml.Name `xml:"Delete"`
	Quiet   bool     `xml:"Quiet"`
	Objects []ObjectToDelete `xml:"Object"`
}

// ObjectToDelete represents an object to delete in DeleteObjects request.
type ObjectToDelete struct {
	Key       string `xml:"Key"`
	VersionId string `xml:"VersionId,omitempty"`
}
package aws

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// CredentialMode identifies the two Bedrock authentication schemes supported
// by the reference channel configuration.
type CredentialMode string

const (
	CredentialModeAKSK   CredentialMode = "ak_sk"
	CredentialModeAPIKey CredentialMode = "api_key"
)

// Credential is a validated, request-local credential. Callers must never
// persist or log its fields.
type Credential struct {
	Mode      CredentialMode
	AccessKey string
	SecretKey string
	APIKey    string
	Region    string
}

// ParseCredential accepts exactly apiKey|region or accessKey|secretKey|region.
// If configuredMode is non-empty, the shape must agree with it.
func ParseCredential(raw string, configuredMode CredentialMode) (Credential, error) {
	if raw == "" || len(raw) > MaxCredentialBytes || strings.ContainsAny(raw, "\r\n\x00") {
		return Credential{}, errors.New("AWS credential is invalid")
	}
	parts := strings.Split(raw, "|")
	for index := range parts {
		if parts[index] == "" || parts[index] != strings.TrimSpace(parts[index]) ||
			len(parts[index]) > MaxCredentialPartBytes || strings.ContainsAny(parts[index], "\r\n\x00") {
			return Credential{}, errors.New("AWS credential is invalid")
		}
	}
	var credential Credential
	switch len(parts) {
	case 2:
		credential = Credential{Mode: CredentialModeAPIKey, APIKey: parts[0], Region: parts[1]}
	case 3:
		credential = Credential{Mode: CredentialModeAKSK, AccessKey: parts[0], SecretKey: parts[1], Region: parts[2]}
	default:
		return Credential{}, errors.New("AWS credential must be apiKey|region or accessKey|secretKey|region")
	}
	if configuredMode != "" && configuredMode != credential.Mode {
		return Credential{}, errors.New("AWS credential shape does not match aws_key_type")
	}
	if err := validateRegion(credential.Region); err != nil {
		return Credential{}, err
	}
	return credential, nil
}

func validateRegion(region string) error {
	if len(region) == 0 || len(region) > MaxRegionBytes || region[0] == '-' || region[len(region)-1] == '-' {
		return errors.New("AWS region is invalid")
	}
	for _, character := range region {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return errors.New("AWS region is invalid")
		}
	}
	if !strings.ContainsRune(region, '-') {
		return errors.New("AWS region is invalid")
	}
	return nil
}

// SignRequestAt applies an AWS Signature Version 4 authorization header for
// the Bedrock service. The explicit timestamp keeps the primitive fully
// deterministic in tests and lets an adapter use one consistent clock sample.
func SignRequestAt(request *http.Request, body []byte, credential Credential, now time.Time) error {
	if request == nil || request.URL == nil {
		return errors.New("AWS request is nil")
	}
	if credential.Mode != CredentialModeAKSK || !validCredentialPart(credential.AccessKey) || !validCredentialPart(credential.SecretKey) {
		return errors.New("AWS access-key signing credential is invalid")
	}
	if err := validateRegion(credential.Region); err != nil {
		return err
	}
	if request.URL.Scheme != "https" || request.URL.Host == "" || request.URL.User != nil {
		return errors.New("AWS signing URL is invalid")
	}
	if int64(len(body)) > MaxRequestBodyBytes {
		return fmt.Errorf("AWS request exceeds %d bytes", MaxRequestBodyBytes)
	}

	now = now.UTC()
	amzDate := now.Format("20060102T150405Z")
	shortDate := now.Format("20060102")
	payloadHash := sha256Hex(body)
	host := strings.ToLower(request.URL.Host)
	canonicalHeaders := "content-type:" + canonicalHeaderValue(request.Header.Get("Content-Type")) + "\n" +
		"host:" + canonicalHeaderValue(host) + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	const signedHeaders = "content-type;host;x-amz-content-sha256;x-amz-date"
	canonicalRequest := request.Method + "\n" + canonicalURI(request.URL) + "\n" + canonicalQuery(request.URL.Query()) + "\n" +
		canonicalHeaders + "\n" + signedHeaders + "\n" + payloadHash
	credentialScope := shortDate + "/" + credential.Region + "/bedrock/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + credentialScope + "\n" + sha256Hex([]byte(canonicalRequest))
	dateKey := hmacSHA256([]byte("AWS4"+credential.SecretKey), shortDate)
	regionKey := hmacSHA256(dateKey, credential.Region)
	serviceKey := hmacSHA256(regionKey, "bedrock")
	signingKey := hmacSHA256(serviceKey, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))

	request.Header.Set("X-Amz-Date", amzDate)
	request.Header.Set("X-Amz-Content-Sha256", payloadHash)
	request.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+credential.AccessKey+"/"+credentialScope+
		", SignedHeaders="+signedHeaders+", Signature="+signature)
	return nil
}

func requestBody(request *http.Request) ([]byte, error) {
	if request == nil || request.Body == nil {
		return nil, errors.New("AWS request body is nil")
	}
	var reader io.ReadCloser
	if request.GetBody != nil {
		copyOfBody, err := request.GetBody()
		if err != nil {
			return nil, errors.New("copy AWS request body")
		}
		reader = copyOfBody
	} else {
		reader = request.Body
	}
	body, err := io.ReadAll(io.LimitReader(reader, MaxRequestBodyBytes+1))
	if request.GetBody != nil {
		_ = reader.Close()
	}
	if err != nil {
		return nil, errors.New("read AWS request body")
	}
	if int64(len(body)) > MaxRequestBodyBytes {
		return nil, fmt.Errorf("AWS request exceeds %d bytes", MaxRequestBodyBytes)
	}
	if request.GetBody == nil {
		request.Body = io.NopCloser(bytes.NewReader(body))
	}
	return body, nil
}

func canonicalURI(value *url.URL) string {
	path := value.EscapedPath()
	if path == "" {
		return "/"
	}
	// SigV4 URI-encodes the already escaped request path for services other
	// than S3. This intentionally encodes ':' as %3A and an existing '%' as
	// %25, matching the AWS SDK's double-encoding behavior for model ARNs.
	return awsPercentEncode(path, true)
}

func canonicalQuery(values url.Values) string {
	type encodedQueryEntry struct{ key, value string }
	entries := make([]encodedQueryEntry, 0, len(values))
	for key, rawValues := range values {
		encodedKey := awsPercentEncode(key, false)
		rawValues = append([]string(nil), rawValues...)
		if len(rawValues) == 0 {
			rawValues = []string{""}
		}
		for _, value := range rawValues {
			entries = append(entries, encodedQueryEntry{key: encodedKey, value: awsPercentEncode(value, false)})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].key == entries[j].key {
			return entries[i].value < entries[j].value
		}
		return entries[i].key < entries[j].key
	})
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		parts = append(parts, entry.key+"="+entry.value)
	}
	return strings.Join(parts, "&")
}

func validCredentialPart(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && len(value) <= MaxCredentialPartBytes &&
		!strings.ContainsAny(value, "|\r\n\x00")
}

func awsPercentEncode(value string, preserveSlash bool) string {
	const hexadecimal = "0123456789ABCDEF"
	var encoded strings.Builder
	encoded.Grow(len(value))
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("-._~", rune(character)) ||
			(preserveSlash && character == '/') {
			encoded.WriteByte(character)
			continue
		}
		encoded.WriteByte('%')
		encoded.WriteByte(hexadecimal[character>>4])
		encoded.WriteByte(hexadecimal[character&0x0f])
	}
	return encoded.String()
}

func canonicalHeaderValue(value string) string { return strings.Join(strings.Fields(value), " ") }

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func hmacSHA256(key []byte, value string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)
}

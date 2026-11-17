// SPDX-License-Identifier: GPL-3.0-or-later

package laohu

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// requestTimeout bounds one authentication round trip. NewClient caps the
// injected http.Client as well, but the deadline lives on the request context
// so a shared client cannot extend it.
const requestTimeout = 20 * time.Second

// advertisedEncoding lists exactly the encodings the transport decodes itself.
// Responses are never handed to net/http's transparent gzip support: the size
// cap must apply to the decoded bytes, not to the compressed ones.
const advertisedEncoding = "gzip, deflate"

// errResponseInvalid reports a response the transport refuses to hand to a
// parser: an unsupported or broken encoding, or a body above the caller's cap.
var errResponseInvalid = &Error{Phase: PhaseInvalidResponse}

// request performs one JSON API call and returns at most maxBytes decoded
// bytes. body is sent unchanged; userAgent is set only when non-empty; envelope
// marks the encrypted game-login exchange, which carries the fixed China-server
// application id as a header instead of a form field. The caller owns the
// returned buffer and must clear it when finished.
func (c *Client) request(ctx context.Context, method, target string, body []byte, userAgent string, maxBytes int64, envelope bool) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if ctx.Err() != nil {
		return nil, &Error{Phase: PhaseCancelled}
	}
	// A non-nil body keeps the RoundTripper contract (Body is always non-nil)
	// even for the bodyless password-state query.
	var payload io.Reader = http.NoBody
	if len(body) > 0 {
		payload = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, payload)
	if err != nil {
		return nil, &Error{Phase: PhaseRequestFailed}
	}
	// Account identities and channel secrets only ever travel over TLS.
	if req.URL.Scheme != "https" || req.URL.Host == "" {
		return nil, &Error{Phase: PhaseRequestFailed}
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", advertisedEncoding)
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if userAgent != "" {
		req.Header.Set("User-Agent", userAgent)
	}
	if envelope {
		req.Header.Set("appId", cnAppID)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, transportFailure(ctx, err)
	}
	defer resp.Body.Close()
	raw, readErr := readResponse(resp, maxBytes)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if readErr != nil {
			if ctx.Err() != nil {
				return nil, &Error{Phase: PhaseCancelled}
			}
			return nil, &Error{Phase: PhaseHTTPFailure, StatusCode: resp.StatusCode}
		}
		return nil, httpFailure(resp.StatusCode, raw)
	}
	if readErr != nil {
		if ctx.Err() != nil {
			return nil, &Error{Phase: PhaseCancelled}
		}
		return nil, readErr
	}
	return raw, nil
}

// transportFailure maps a net/http error without wrapping it: the wrapped error
// can embed the request URL (and therefore a phone number) or response bytes.
func transportFailure(ctx context.Context, err error) error {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &Error{Phase: PhaseCancelled}
	}
	return &Error{Phase: PhaseRequestFailed}
}

// readResponse buffers resp within maxBytes, decoding the encodings that
// advertisedEncoding offers. A declared or streamed length above the cap is
// rejected instead of buffered.
func readResponse(resp *http.Response, maxBytes int64) ([]byte, error) {
	if resp.Body == nil || maxBytes <= 0 {
		return nil, errResponseInvalid
	}
	if resp.ContentLength > maxBytes {
		return nil, errResponseInvalid
	}
	wire, err := readCapped(resp.Body, maxBytes)
	if err != nil {
		return nil, err
	}
	if resp.Uncompressed {
		return wire, nil
	}
	encoding := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	if encoding == "" || encoding == "identity" {
		return wire, nil
	}
	// The compressed copy is only an intermediate value: never keep it.
	defer clear(wire)
	if encoding == "gzip" {
		return inflate(wire, maxBytes, func(b []byte) (io.ReadCloser, error) { return gzip.NewReader(bytes.NewReader(b)) })
	}
	if encoding == "deflate" {
		// Servers disagree on "deflate": the HTTP standard means zlib-wrapped
		// data, but older stacks send a raw DEFLATE stream.
		if decoded, zerr := inflate(wire, maxBytes, func(b []byte) (io.ReadCloser, error) { return zlib.NewReader(bytes.NewReader(b)) }); zerr == nil {
			return decoded, nil
		}
		return inflate(wire, maxBytes, func(b []byte) (io.ReadCloser, error) { return flate.NewReader(bytes.NewReader(b)), nil })
	}
	return nil, errResponseInvalid
}

// readCapped buffers at most maxBytes. Reading one byte past the cap is how a
// streamed body without a declared length is rejected.
func readCapped(r io.Reader, maxBytes int64) ([]byte, error) {
	out, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil || int64(len(out)) > maxBytes {
		clear(out)
		return nil, errResponseInvalid
	}
	return out, nil
}

// inflate decodes one compressed body under the same cap as the wire body, so
// a small download cannot expand into an unbounded buffer.
func inflate(compressed []byte, maxBytes int64, open func([]byte) (io.ReadCloser, error)) ([]byte, error) {
	decoder, err := open(compressed)
	if err != nil {
		return nil, errResponseInvalid
	}
	defer decoder.Close()
	out, err := io.ReadAll(io.LimitReader(decoder, maxBytes+1))
	if err != nil || int64(len(out)) > maxBytes {
		clear(out)
		return nil, errResponseInvalid
	}
	return out, nil
}

// httpFailure reports a non-2xx reply. The body is parsed only for the API code
// and a short display hint; Error.Message sanitises that hint and no part of
// the body is ever wrapped into the error or logged.
func httpFailure(status int, raw []byte) error {
	defer clear(raw)
	failure := &Error{Phase: PhaseHTTPFailure, StatusCode: status}
	root, err := strictObject(raw)
	if err != nil {
		return failure
	}
	if code, ok := intValue(root["code"], false); ok {
		failure.APICode = code
		failure.HasAPICode = true
	}
	if message, ok := root["message"].(string); ok {
		failure.explanation = message
	}
	return failure
}

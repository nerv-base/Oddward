// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

var (
	errIdle            = errors.New("mirror idle timeout")
	errRangeIgnored    = errors.New("server ignored range")
	errRange416        = errors.New("range rejected")
	errShortBody       = errors.New("short range body")
	errIdentityChanged = errors.New("conditional identity changed")
)

func urlKey(u *url.URL) string {
	sum := sha256.Sum256([]byte(u.String()))
	return hex.EncodeToString(sum[:])
}
func trustedURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return nil, failure(ErrHTTP, "HTTPS URL", "", nil)
	}
	return u, nil
}
func markFor(marks []ObjectMark, u *url.URL) (ObjectMark, bool) {
	key := urlKey(u)
	for _, m := range marks {
		if m.Host == u.Host && m.URLKey == key {
			return m, true
		}
	}
	return ObjectMark{}, false
}
func setConditional(req *http.Request, marks []ObjectMark) {
	req.Header.Del("If-Range")
	if mark, ok := markFor(marks, req.URL); ok {
		if mark.ETag != "" && !strings.HasPrefix(mark.ETag, "W/") {
			req.Header.Set("If-Range", mark.ETag)
		} else if mark.LastModified != "" {
			req.Header.Set("If-Range", mark.LastModified)
		}
	}
}
func (f *fileTransfer) client() *http.Client {
	client := *f.engine.options.Client
	previous := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return failure(ErrHTTP, "redirect limit", "", nil)
		}
		if _, err := trustedURL(req.URL.String()); err != nil {
			return err
		}
		f.mu.Lock()
		marks := append([]ObjectMark(nil), f.transfer.Marks...)
		f.mu.Unlock()
		setConditional(req, marks)
		if previous != nil {
			return previous(req, via)
		}
		return nil
	}
	return &client
}
func (f *fileTransfer) request(ctx context.Context, method, raw string, start, end int64, sequential bool) (*http.Response, func(), error) {
	u, err := trustedURL(raw)
	if err != nil {
		return nil, func() {}, err
	}
	if err := f.engine.admission.acquire(ctx, f); err != nil {
		return nil, func() {}, classify("request admission", f.transfer.Object.String(), err)
	}
	select {
	case f.engine.connections <- struct{}{}:
	case <-ctx.Done():
		f.engine.admission.release(f)
		return nil, func() {}, classify("connection queue", f.transfer.Object.String(), ctx.Err())
	}
	f.progress.beginNetwork()
	reqCtx, cancel := context.WithCancelCause(ctx)
	timer := time.AfterFunc(30*time.Second, func() { cancel(errIdle) })
	cleanup := func() {
		timer.Stop()
		cancel(nil)
		f.progress.endNetwork()
		<-f.engine.connections
		f.engine.admission.release(f)
	}
	req, err := http.NewRequestWithContext(reqCtx, method, u.String(), nil)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", f.engine.options.UserAgent)
	if method == "GET" && !sequential {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
		f.mu.Lock()
		marks := append([]ObjectMark(nil), f.transfer.Marks...)
		f.mu.Unlock()
		setConditional(req, marks)
	}
	resp, err := f.client().Do(req)
	if err != nil {
		cause := context.Cause(reqCtx)
		cleanup()
		if errors.Is(cause, errIdle) {
			return nil, func() {}, &TaskError{Kind: ErrNetwork, Host: u.Host, Op: "idle GET", Path: f.transfer.Object.String(), Err: errIdle}
		}
		var classified *TaskError
		if errors.As(err, &classified) {
			return nil, func() {}, classified
		}
		return nil, func() {}, &TaskError{Kind: ErrNetwork, Host: u.Host, Op: "range get", Path: f.transfer.Object.String(), Err: err}
	}
	// Continue the idle deadline while reading instead of bounding the duration
	// of a rate-limited whole-object fallback.
	resp.Body = &idleBody{ReadCloser: resp.Body, timer: timer, ctx: reqCtx}
	return resp, cleanup, nil
}

type idleBody struct {
	io.ReadCloser
	timer *time.Timer
	ctx   context.Context
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.timer.Reset(30 * time.Second)
	}
	return n, err
}
func (f *fileTransfer) head(ctx context.Context, raw string) error {
	resp, cleanup, err := f.request(ctx, "HEAD", raw, 0, 0, true)
	if err != nil {
		return err
	}
	defer cleanup()
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.ContentLength != f.transfer.Source.Object.Size || !identityEncoding(resp) {
		return &TaskError{Kind: ErrRangeMismatch, Host: resp.Request.URL.Host, HTTPStatus: resp.StatusCode, Op: "mirror HEAD", Path: f.transfer.Object.String()}
	}
	return nil
}
func identityEncoding(resp *http.Response) bool {
	return !resp.Uncompressed && (resp.Header.Get("Content-Encoding") == "" || resp.Header.Get("Content-Encoding") == "identity")
}
func (f *fileTransfer) observe(resp *http.Response) error {
	u := resp.Request.URL
	mark := ObjectMark{Host: u.Host, URLKey: urlKey(u), ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified"), ObservedAt: time.Now().UTC()}
	if len(mark.ETag) > 1024 || len(mark.LastModified) > 128 {
		return failure(ErrRangeMismatch, "validator length", f.transfer.Object.String(), nil)
	}
	f.mu.Lock()
	old, exists := markFor(f.transfer.Marks, u)
	if exists && old.ETag != "" && mark.ETag != "" && old.ETag != mark.ETag {
		f.mu.Unlock()
		return errIdentityChanged
	}
	if exists && old.ETag == mark.ETag && old.LastModified == mark.LastModified {
		f.mu.Unlock()
		return nil
	}
	if exists {
		for i := range f.transfer.Marks {
			if f.transfer.Marks[i].Host == mark.Host && f.transfer.Marks[i].URLKey == mark.URLKey {
				f.transfer.Marks[i] = mark
				break
			}
		}
	} else {
		if len(f.transfer.Marks) >= 64 {
			f.mu.Unlock()
			return failure(ErrRecoveryRequired, "too many validators", f.transfer.Object.String(), nil)
		}
		f.transfer.Marks = append(f.transfer.Marks, mark)
	}
	snapshot := f.snapshotLocked()
	f.mu.Unlock()
	return f.save(snapshot)
}
func (f *fileTransfer) fetch(ctx context.Context, raw string, start, length, destination int64, sequential bool) (string, error) {
	end := start + length - 1
	resp, cleanup, err := f.request(ctx, "GET", raw, start, end, sequential)
	if err != nil {
		return "", err
	}
	defer cleanup()
	defer resp.Body.Close()
	taskErr := func(kind ErrorKind, op string, cause error) error {
		return &TaskError{Kind: kind, Path: f.transfer.Object.String(), Host: resp.Request.URL.Host, HTTPStatus: resp.StatusCode, Op: op, Err: cause}
	}
	if resp.StatusCode == 416 {
		return "", taskErr(ErrRangeRejected, "range get", errRange416)
	}
	if resp.StatusCode == 200 && !sequential {
		return "", taskErr(ErrRangeMismatch, "range ignored", errRangeIgnored)
	}
	if (sequential && resp.StatusCode != 200) || (!sequential && resp.StatusCode != 206) {
		if resp.StatusCode == 429 || resp.StatusCode == 503 {
			if delay := retryAfter(resp.Header.Get("Retry-After")); delay > 0 {
				if err := waitDelay(ctx, delay); err != nil {
					return "", err
				}
			}
		}
		kind := ErrHTTP
		if f.transfer.Source.Kind == 3 && (resp.StatusCode == 404 || resp.StatusCode == 410) {
			kind = ErrPatchBlobMissing
		}
		return "", taskErr(kind, "range get", nil)
	}
	expected := fmt.Sprintf("bytes %d-%d/%d", start, end, f.transfer.Source.Object.Size)
	if (!sequential && resp.Header.Get("Content-Range") != expected) || resp.ContentLength != length || !identityEncoding(resp) {
		return "", taskErr(ErrRangeMismatch, "response envelope", nil)
	}
	if err = f.observe(resp); err != nil {
		return "", err
	}
	hash := md5.New()
	ptr := buffers.Get().(*[]byte)
	defer buffers.Put(ptr)
	buffer := *ptr
	var copied int64
	for copied < length {
		want := int(min(int64(len(buffer)), length-copied))
		// Waiting for this engine's rate budget is not remote inactivity.
		body := resp.Body.(*idleBody)
		body.timer.Stop()
		if err = f.engine.limiter.wait(ctx, want); err != nil {
			return "", classify("rate wait", f.transfer.Object.String(), err)
		}
		body.timer.Reset(30 * time.Second)
		n, readErr := io.ReadFull(resp.Body, buffer[:want])
		if n > 0 {
			f.progress.addIO(n, false)
			hash.Write(buffer[:n])
			written, writeErr := f.file.WriteAt(buffer[:n], destination+copied)
			f.progress.addIO(written, true)
			if writeErr != nil {
				return "", classify("write part", f.transfer.Object.String(), writeErr)
			}
			if written != n {
				return "", classify("write part", f.transfer.Object.String(), io.ErrShortWrite)
			}
			copied += int64(n)
		}
		if readErr != nil {
			if errors.Is(context.Cause(body.ctx), errIdle) {
				return "", taskErr(ErrNetwork, "idle response", errIdle)
			}
			return "", taskErr(ErrNetwork, "short response", errShortBody)
		}
	}
	var extra [1]byte
	n, readErr := resp.Body.Read(extra[:])
	if n != 0 || readErr != io.EOF {
		return "", taskErr(ErrRangeMismatch, "response tail", readErr)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func retryAfter(value string) time.Duration {
	if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		return max(time.Until(at), 0)
	}
	return 0
}
func waitDelay(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func retryable(err error) bool {
	var te *TaskError
	if !errors.As(err, &te) {
		return false
	}
	switch te.Kind {
	case ErrNetwork, ErrRangeMismatch, ErrChecksum:
		return true
	case ErrHTTP:
		return te.HTTPStatus == 408 || te.HTTPStatus == 429 || te.HTTPStatus >= 500
	}
	return false
}
func retryDelay(attempt int) time.Duration {
	return (500*time.Millisecond)<<min(attempt, 4) + time.Duration(rand.IntN(251))*time.Millisecond
}

var _ = os.ErrNotExist

// SPDX-License-Identifier: GPL-3.0-or-later
package laohu

// SignParameters returns the protocol MD5 signature, excluding an existing sign
// field. It does not mutate fields; sign before omitting empty form fields.
func SignParameters(fields map[string]string, key string) string {
	p := parameters(fields)
	copy := make(parameters, len(p))
	for k, v := range p {
		copy[k] = v
	}
	copy.sign(key)
	return copy["sign"]
}

// CanonicalParameters returns sorted, unescaped key=value pairs.
func CanonicalParameters(fields map[string]string) string { return parameters(fields).canonical() }

// FormParameters returns the sorted URL-encoded form, retaining empty fields.
func FormParameters(fields map[string]string) string { return parameters(fields).form() }

// EncryptField applies the channel AES-ECB field encryption without changing
// Config validation or the existing PC channel identity.
func EncryptField(text, key string) (string, error) { return encryptField(text, key) }

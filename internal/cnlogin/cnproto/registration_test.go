// SPDX-License-Identifier: GPL-3.0-or-later

package cnproto

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func profileFor(t *testing.T, version string) Profile {
	t.Helper()
	for _, p := range KnownProfiles() {
		if p.SDKVersion == version {
			return p
		}
	}
	t.Fatalf("no profile for %s", version)
	return Profile{}
}

func TestParseRegistrationDerivesKeys(t *testing.T) {
	for _, version := range []string{"2.0.8.0", "2.0.9.0"} {
		frame := registrationFrame(testGamePID, validReg(version))
		original := append([]byte(nil), frame...)
		reg, err := ParseRegistration(frame, testGamePID, profileFor(t, version))
		if err != nil {
			t.Fatalf("%s: %v", version, err)
		}
		if !bytes.Equal(frame, original) {
			t.Fatalf("%s: input frame modified", version)
		}
		if reg.CallbackWindow != testCallbackWnd {
			t.Fatalf("%s: callback = %#x", version, reg.CallbackWindow)
		}
		// spec §8.1 synthetic vectors.
		tail, err := reg.AppKeyTail()
		if err != nil || string(tail[:]) != "QRSTUVWXYZ012345" {
			t.Fatalf("%s: tail = %q, %v", version, tail[:], err)
		}
		pre, err := reg.PreLoginKey()
		if err != nil || string(pre[:]) != "BCHIJKLPQRZ01234" {
			t.Fatalf("%s: preLogin = %q, %v", version, pre[:], err)
		}
		reg.Zero()
		if _, err := reg.AppKeyTail(); !errors.Is(err, ErrZeroed) {
			t.Fatalf("%s: tail after Zero: %v", version, err)
		}
		if _, err := reg.PreLoginKey(); !errors.Is(err, ErrZeroed) {
			t.Fatalf("%s: preLogin after Zero: %v", version, err)
		}
	}
}

func TestParseRegistrationRejectsFrameShape(t *testing.T) {
	p := profileFor(t, "2.0.8.0")
	good := registrationFrame(testGamePID, validReg("2.0.8.0"))
	wrongOp := append([]byte(nil), good...)
	wrongOp[4] = byte(OpQuery)
	cases := map[string]struct {
		frame []byte
		pid   uint32
	}{
		"short":    {good[:RegistrationSize-1], testGamePID},
		"op":       {wrongOp, testGamePID},
		"zero-pid": {good, 0},
		"size":     {header(OpRegister, RegistrationSize+2), testGamePID},
	}
	for name, c := range cases {
		if _, err := ParseRegistration(c.frame, c.pid, p); !errors.Is(err, ErrFrame) {
			t.Errorf("%s: err = %v, want ErrFrame", name, err)
		}
	}
}

func TestParseRegistrationRejectsMetadata(t *testing.T) {
	p208 := profileFor(t, "2.0.8.0")
	key := func(mutate func([]uint16)) []uint16 {
		units := make([]uint16, 32)
		for i := range units {
			units[i] = uint16(testAppKey[i])
		}
		mutate(units)
		return units
	}
	cases := map[string]struct {
		f   regFields
		pid uint32
		p   Profile
	}{
		"other-profile-version": {validReg("2.0.9.0"), testGamePID, p208},
		"unknown-version":       {validReg("2.0.7.0"), testGamePID, p208},
		"application":           {func() regFields { f := validReg("2.0.8.0"); f.app = "1290"; return f }(), testGamePID, p208},
		"config":                {func() regFields { f := validReg("2.0.8.0"); f.config = "other.config"; return f }(), testGamePID, p208},
		"short-key":             {func() regFields { f := validReg("2.0.8.0"); f.key = testAppKey[:31]; return f }(), testGamePID, p208},
		"no-terminator":         {func() regFields { f := validReg("2.0.8.0"); f.noKeyTerminator = true; return f }(), testGamePID, p208},
		"control-char": {func() regFields {
			f := validReg("2.0.8.0")
			f.keyUnits = key(func(u []uint16) { u[5] = 0x1F })
			return f
		}(), testGamePID, p208},
		"del-char": {func() regFields {
			f := validReg("2.0.8.0")
			f.keyUnits = key(func(u []uint16) { u[30] = 0x7F })
			return f
		}(), testGamePID, p208},
		"wide-char": {func() regFields {
			f := validReg("2.0.8.0")
			f.keyUnits = key(func(u []uint16) { u[0] = 0x4E41 })
			return f
		}(), testGamePID, p208},
		"other-client-xor": {validReg("2.0.8.0"), testGamePID + 1, p208},
		"empty-profile":    {validReg("2.0.8.0"), testGamePID, Profile{}},
	}
	for name, c := range cases {
		frame := registrationFrame(testGamePID, c.f)
		_, err := ParseRegistration(frame, c.pid, c.p)
		if !errors.Is(err, ErrRegistration) {
			t.Errorf("%s: err = %v, want ErrRegistration", name, err)
			continue
		}
		if strings.Contains(err.Error(), testAppKey[:8]) || strings.Contains(err.Error(), testAppKey[16:]) {
			t.Errorf("%s: error leaks key material: %v", name, err)
		}
	}
}

func TestRegistrationFormattingRedactsKey(t *testing.T) {
	reg, err := ParseRegistration(registrationFrame(testGamePID, validReg("2.0.9.0")), testGamePID, profileFor(t, "2.0.9.0"))
	if err != nil {
		t.Fatal(err)
	}
	out := fmt.Sprintf("%v|%+v|%#v|%s|%x|%q|%v|%+v", reg, reg, reg, reg, reg, reg, *reg, []*Registration{reg})
	for _, secret := range []string{testAppKey[:8], testAppKey[16:], "BCHIJKLPQRZ01234", "51 52 53 54", "5152535455"} {
		if strings.Contains(out, secret) {
			t.Fatalf("formatted registration leaks %q: %s", secret, out)
		}
	}
	if !strings.Contains(out, "2.0.9.0") {
		t.Fatalf("formatted registration lacks public profile: %s", out)
	}
}

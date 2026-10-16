package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters follow the OWASP recommendation for interactive logins
// (m=19 MiB, t=2, p=1). Keeping the working set small matters on the 1 vCPU /
// 1 GiB admin node the console is designed for.
const (
	argonTime    = 2
	argonMemory  = 19 * 1024
	argonThreads = 1
	argonKeyLen  = 32
	argonSaltLen = 16
)

// ErrInvalidHash is returned when a stored password hash cannot be parsed.
var ErrInvalidHash = errors.New("invalid password hash")

// hashSlots bounds concurrent argon2id computations, so a burst of logins
// cannot exhaust memory on a 1 GiB admin node.
var hashSlots = make(chan struct{}, 2)

// derive runs argon2id and then returns its working memory to the OS, keeping
// the idle footprint of the process small.
func derive(password string, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte {
	hashSlots <- struct{}{}
	sum := argon2.IDKey([]byte(password), salt, time, memory, threads, keyLen)
	<-hashSlots
	debug.FreeOSMemory()
	return sum
}

// HashPassword derives an argon2id hash in the standard encoded form.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("read salt: %w", err)
	}
	sum := derive(password, salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(sum)), nil
}

// VerifyPassword reports whether password matches the encoded argon2id hash.
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, ErrInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, ErrInvalidHash
	}
	var memory uint32
	var time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false, ErrInvalidHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, ErrInvalidHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, ErrInvalidHash
	}
	got := derive(password, salt, time, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

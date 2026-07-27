package workspace

import (
	"crypto/rand"
	"fmt"
	"io"
	"time"
)

func newUUIDv7() (string, error) {
	return uuidv7(time.Now(), rand.Reader)
}

func uuidv7(now time.Time, random io.Reader) (string, error) {
	var value [16]byte
	milliseconds := now.UnixMilli()
	if milliseconds < 0 || milliseconds > 1<<48-1 {
		return "", fmt.Errorf("uuidv7 timestamp out of range: %d", milliseconds)
	}
	if _, err := io.ReadFull(random, value[6:]); err != nil {
		return "", fmt.Errorf("allocate uuidv7 randomness: %w", err)
	}
	value[0] = byte(milliseconds >> 40)
	value[1] = byte(milliseconds >> 32)
	value[2] = byte(milliseconds >> 24)
	value[3] = byte(milliseconds >> 16)
	value[4] = byte(milliseconds >> 8)
	value[5] = byte(milliseconds)
	value[6] = value[6]&0x0f | 0x70
	value[8] = value[8]&0x3f | 0x80
	return fmt.Sprintf(
		"%08x-%04x-%04x-%04x-%012x",
		value[0:4],
		value[4:6],
		value[6:8],
		value[8:10],
		value[10:16],
	), nil
}

// Package codecは上限を明示した厳格なCtrlCmd wire codecを実装する。
package codec

import "fmt"

// Categoryはcodec境界で安定して扱うerror分類である。
type Category string

const (
	Truncated      Category = "TRUNCATED"
	Malformed      Category = "MALFORMED"
	OverLimit      Category = "OVER_LIMIT"
	Unsupported    Category = "UNSUPPORTED"
	Timeout        Category = "TIMEOUT"
	Saturated      Category = "SATURATED"
	PeerDisconnect Category = "PEER_DISCONNECT"
	Internal       Category = "INTERNAL"
)

// Errorはpayloadを保持せず、分類・理由・位置・sizeだけを返す境界errorである。
type Error struct {
	Category Category
	Reason   string
	Offset   int
	Size     int64
}

// Errorはlogやtestで使用する上限付きの診断文字列を返す。
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("ctrlcmd codec: %s/%s offset=%d size=%d", e.Category, e.Reason, e.Offset, e.Size)
}

func failure(category Category, reason string, offset int, size int64) error {
	if len(reason) > 96 {
		reason = "reason-over-limit"
		category = Internal
	}
	return &Error{Category: category, Reason: reason, Offset: offset, Size: size}
}

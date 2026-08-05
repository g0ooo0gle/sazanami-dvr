package codec

const (
	HeaderSize            = 8
	MaxRequestBody        = 1 * 1024 * 1024
	MaxResponseBody       = 256 * 1024 * 1024
	MaxStructureExtent    = 16 * 1024 * 1024
	MaxStringExtent       = 256 * 1024
	MaxVectorElements     = 65_536
	MaxDepth              = 12
	MaxLogicalItems       = 1_048_576
	InitialCommandVersion = 5
)

// Limitsは1 request内のmemory使用量、要素数、nestingを制限する。
// zero値はDefaultLimitsへ正規化され、hard limitを緩める指定は受理しない。
type Limits struct {
	RequestBody     int
	ResponseBody    int
	StructureExtent int
	StringExtent    int
	VectorElements  int
	Depth           int
	LogicalItems    int
}

// DefaultLimitsは製品仕様で定めたhard limit一式を返す。
func DefaultLimits() Limits {
	return Limits{
		RequestBody:     MaxRequestBody,
		ResponseBody:    MaxResponseBody,
		StructureExtent: MaxStructureExtent,
		StringExtent:    MaxStringExtent,
		VectorElements:  MaxVectorElements,
		Depth:           MaxDepth,
		LogicalItems:    MaxLogicalItems,
	}
}

func (l Limits) normalized() (Limits, error) {
	hard := DefaultLimits()
	values := []*int{&l.RequestBody, &l.ResponseBody, &l.StructureExtent, &l.StringExtent, &l.VectorElements, &l.Depth, &l.LogicalItems}
	hards := []int{hard.RequestBody, hard.ResponseBody, hard.StructureExtent, hard.StringExtent, hard.VectorElements, hard.Depth, hard.LogicalItems}
	for i, value := range values {
		if *value == 0 {
			*value = hards[i]
		}
		if *value < 1 || *value > hards[i] {
			return Limits{}, failure(OverLimit, "effective-limit", 0, int64(*value))
		}
	}
	return l, nil
}

package recording

import (
	"reflect"
	"testing"
)

func TestQualitySummaryValidation(t *testing.T) {
	if (QualitySummary{}).Validate() != nil {
		t.Fatal("legacy zero must be valid")
	}
	for _, text := range []string{"UNKNOWN", "NO_ISSUES_OBSERVED", "DEGRADED"} {
		status, err := ParseQualityStatus(text)
		if err != nil || status.String() != text {
			t.Fatalf("status=%v err=%v", status, err)
		}
	}
	if _, err := ParseQualityStatus("SUCCESS"); err == nil {
		t.Fatal("unknown status accepted")
	}
	if (QualitySummary{Status: QualityStatus(255)}).Validate() == nil {
		t.Fatal("invalid enum accepted")
	}
	typ := reflect.TypeOf(QualitySummary{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.Type.Kind() != reflect.Int64 {
			continue
		}
		t.Run(field.Name, func(t *testing.T) {
			upper := int64(2147483647)
			if field.Name == "FallbackEvents" {
				upper = 1
			}
			if field.Name == "ReconnectCount" {
				upper = 3
			}
			for _, value := range []int64{-1, 0, upper, upper + 1} {
				var q QualitySummary
				reflect.ValueOf(&q).Elem().Field(i).SetInt(value)
				if (q.Validate() == nil) != (value >= 0 && value <= upper) {
					t.Fatalf("value=%d", value)
				}
			}
		})
	}
}

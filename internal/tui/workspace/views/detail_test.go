package views

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/basecamp/basecamp-cli/internal/tui"
	"github.com/basecamp/basecamp-cli/internal/tui/workspace/widget"
)

func testDetail(originView, originHint string) *Detail {
	styles := tui.NewStyles()
	return &Detail{
		styles:        styles,
		recordingID:   100,
		recordingType: "Todo",
		originView:    originView,
		originHint:    originHint,
		preview:       widget.NewPreview(styles),
		data: &detailData{
			title:      "Test Todo",
			recordType: "Todo",
			creator:    "Alice",
		},
	}
}

func TestDetail_OriginContext_RenderedInPreview(t *testing.T) {
	v := testDetail("Activity", "completed Todo")
	v.syncPreview()

	fields := v.preview.Fields()
	found := false
	for _, f := range fields {
		if f.Key == "From" {
			found = true
			assert.Equal(t, "Activity · completed Todo", f.Value)
			break
		}
	}
	assert.True(t, found, "preview should contain From field when origin is set")
}

func TestDetail_NoOrigin_NoFromField(t *testing.T) {
	v := testDetail("", "")
	v.syncPreview()

	fields := v.preview.Fields()
	for _, f := range fields {
		assert.NotEqual(t, "From", f.Key, "preview should not contain From field when origin is empty")
	}
}

func TestDetail_OriginViewOnly_NoHint(t *testing.T) {
	v := testDetail("Home", "")
	v.syncPreview()

	fields := v.preview.Fields()
	found := false
	for _, f := range fields {
		if f.Key == "From" {
			found = true
			assert.Equal(t, "Home", f.Value)
			break
		}
	}
	assert.True(t, found, "preview should contain From field with just origin view")
}

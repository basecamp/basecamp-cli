package summarize

// ZoomLevel represents a target character budget for summaries.
type ZoomLevel int

const (
	Zoom40   ZoomLevel = 40   // Ticker line
	Zoom80   ZoomLevel = 80   // One-liner
	Zoom200  ZoomLevel = 200  // 2-3 line preview
	Zoom500  ZoomLevel = 500  // Paragraph
	Zoom1000 ZoomLevel = 1000 // Full digest
)

// BucketZoom normalizes a target char count to the nearest zoom bucket.
func BucketZoom(targetChars int) ZoomLevel {
	switch {
	case targetChars <= 40:
		return Zoom40
	case targetChars <= 80:
		return Zoom80
	case targetChars <= 200:
		return Zoom200
	case targetChars <= 500:
		return Zoom500
	default:
		return Zoom1000
	}
}

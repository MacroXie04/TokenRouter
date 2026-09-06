package quota

// SafeFloat64 guards against NaN/Inf when converting to float64 for display.
func SafeFloat64(f float64) float64 {
	if f != f || f > 1e300 || f < -1e300 {
		return 0
	}
	return f
}

// ClampInt bounds v into [min, max].
func ClampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

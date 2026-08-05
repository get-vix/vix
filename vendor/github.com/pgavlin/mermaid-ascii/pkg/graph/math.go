package graph

// Min returns the smaller of two integers.
func Min(x, y int) int {
	if x < y {
		return x
	}
	return y
}

// Max returns the larger of two integers.
func Max(x, y int) int {
	if x > y {
		return x
	}
	return y
}

// Abs returns the absolute value of an integer.
func Abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// CeilDiv returns the ceiling of the integer division x/y.
func CeilDiv(x, y int) int {
	if x%y == 0 {
		return x / y
	}
	return x/y + 1
}

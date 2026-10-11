package evaluation

import (
	"errors"
	"math"
)

// Sum binomial mass in log space. Exact here means exact-binomial inversion,
// evaluated with bounded floating-point arithmetic rather than an approximation.
func binomialRange(n, lo, hi int, p float64) float64 {
	if lo > hi {
		return 0
	}
	if p <= 0 {
		if lo == 0 {
			return 1
		}
		return 0
	}
	if p >= 1 {
		if hi == n {
			return 1
		}
		return 0
	}
	lg, _ := math.Lgamma(float64(n + 1))
	sum, correction := 0., 0.
	for k := lo; k <= hi; k++ {
		a, _ := math.Lgamma(float64(k + 1))
		b, _ := math.Lgamma(float64(n - k + 1))
		mass := math.Exp(lg - a - b + float64(k)*math.Log(p) + float64(n-k)*math.Log1p(-p))
		y := mass - correction
		next := sum + y
		correction = (next - sum) - y
		sum = next
	}
	return min(1., sum)
}
func lowerExactBinomial(k, n int, alpha float64) float64 {
	if k == 0 {
		return 0
	}
	lo, hi := 0., 1.
	for range 80 {
		mid := (lo + hi) / 2
		if binomialRange(n, k, n, mid) < alpha {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2
}
func upperExactBinomial(k, n int, alpha float64) float64 {
	if k == n {
		return 1
	}
	lo, hi := 0., 1.
	for range 80 {
		mid := (lo + hi) / 2
		if binomialRange(n, 0, k, mid) > alpha {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2
}
func QualityInterval(wins, losses, n, requiredStrata int) (float64, error) {
	if n <= 0 || n > 10000 || wins < 0 || losses < 0 || wins+losses > n || requiredStrata < 1 {
		return 0, errors.New("invalid paired confidence counts")
	}
	alpha := .05 / (2 * float64(requiredStrata))
	return lowerExactBinomial(wins, n, alpha) - upperExactBinomial(losses, n, alpha), nil
}

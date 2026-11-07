package taxonomy

import "math"

/* verify.go 用到的底层数学函数：全部只依赖标准库 math。
   独立成文件以便 verify.go 保持纯解释器逻辑。 */

func mathSqrt(x float64) float64 { return math.Sqrt(x) }

func floatLog(x float64) float64 {
	if x <= 0 {
		return math.NaN()
	}
	return math.Log(x)
}

func floatAtan2(y, x float64) float64 { return math.Atan2(y, x) }

func floatPow(x, y float64) float64 { return math.Pow(x, y) }

func floatFn1(name string, x float64) float64 {
	switch name {
	case "sin":
		return math.Sin(x)
	case "cos":
		return math.Cos(x)
	case "tan":
		return math.Tan(x)
	case "asin":
		return math.Asin(x)
	case "acos":
		return math.Acos(x)
	case "atan":
		return math.Atan(x)
	case "exp":
		return math.Exp(x)
	case "log10":
		return math.Log10(x)
	case "log2":
		return math.Log2(x)
	case "degrees":
		return x * 180 / math.Pi
	case "radians":
		return x * math.Pi / 180
	}
	return math.NaN()
}

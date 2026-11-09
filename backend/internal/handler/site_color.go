package handler

import (
	"errors"
	"math"
	"regexp"
)

var primaryColorRe = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// parseHexColor 把 #RRGGBB 拆成 0-255 的三个分量。
func parseHexColor(hex string) (r, g, b float64, err error) {
	if !primaryColorRe.MatchString(hex) {
		return 0, 0, 0, errors.New("主色要写成 #RRGGBB 的色号")
	}
	var v [3]float64
	for i := 0; i < 3; i++ {
		n := 0
		for _, ch := range hex[1+i*2 : 3+i*2] {
			switch {
			case ch >= '0' && ch <= '9':
				n = n*16 + int(ch-'0')
			case ch >= 'a' && ch <= 'f':
				n = n*16 + int(ch-'a') + 10
			default:
				n = n*16 + int(ch-'A') + 10
			}
		}
		v[i] = float64(n)
	}
	return v[0], v[1], v[2], nil
}

func relativeLuminance(hex string) (float64, error) {
	r, g, b, err := parseHexColor(hex)
	if err != nil {
		return 0, err
	}
	lin := func(c float64) float64 {
		c /= 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b), nil
}

// contrastRatio 按 WCAG 2.x 公式算两个 #RRGGBB 颜色的对比度（1 到 21）。
func contrastRatio(a, b string) (float64, error) {
	la, err := relativeLuminance(a)
	if err != nil {
		return 0, err
	}
	lb, err := relativeLuminance(b)
	if err != nil {
		return 0, err
	}
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05), nil
}

// validPrimaryColor 校验主色：格式正确，且按钮上的白字对比度不低于 4.5。
func validPrimaryColor(hex string) error {
	ratio, err := contrastRatio("#FFFFFF", hex)
	if err != nil {
		return err
	}
	if ratio < 4.5 {
		return errors.New("这个颜色太浅，按钮上的白字看不清，请选深一点的颜色")
	}
	return nil
}

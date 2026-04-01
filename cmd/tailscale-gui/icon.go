package main

var iconConnected = generateICO(0x00, 0xC8, 0x50)
var iconDisconnected = generateICO(0x99, 0x99, 0x99)

func generateICO(r, g, b byte) []byte {
	ico := []byte{0, 0, 1, 0, 1, 0}
	width := byte(16)
	height := byte(16)
	bmpDataSize := 40 + 16*16*4 + 16*16/8
	dataOffset := 6 + 16
	ico = append(ico, width, height, 0, 0, 1, 0, 32, 0,
		byte(bmpDataSize), byte(bmpDataSize>>8), byte(bmpDataSize>>16), byte(bmpDataSize>>24),
		byte(dataOffset), byte(dataOffset>>8), byte(dataOffset>>16), byte(dataOffset>>24))
	bmpHeader := []byte{40, 0, 0, 0, 16, 0, 0, 0, 32, 0, 0, 0, 1, 0, 32, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	ico = append(ico, bmpHeader...)
	cx, cy := 7.5, 7.5
	radius := 6.0
	for y := 15; y >= 0; y-- {
		for x := 0; x < 16; x++ {
			dx := float64(x) - cx
			dy := float64(y) - cy
			dist := dx*dx + dy*dy
			if dist <= radius*radius {
				alpha := byte(255)
				if dist > (radius-1)*(radius-1) {
					alpha = 180
				}
				ico = append(ico, b, g, r, alpha)
			} else {
				ico = append(ico, 0, 0, 0, 0)
			}
		}
	}
	for y := 0; y < 16; y++ {
		ico = append(ico, 0, 0, 0, 0)
	}
	return ico
}

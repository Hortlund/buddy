package budmodel

import (
	"image"
	"image/color"
	"math"
)

type transform struct {
	xAxis, yAxis, zAxis vector
	location            vector
}

func (t transform) rotate(v vector) vector {
	return vector{
		t.xAxis.x*v.x + t.yAxis.x*v.y + t.zAxis.x*v.z,
		t.xAxis.y*v.x + t.yAxis.y*v.y + t.zAxis.y*v.z,
		t.xAxis.z*v.x + t.yAxis.z*v.y + t.zAxis.z*v.z,
	}
}

func rotation(yaw, pitch, roll float32) transform {
	sy, cy := sincos(yaw)
	sx, cx := sincos(pitch)
	sz, cz := sincos(roll)
	return transform{
		xAxis: vector{cz*cy - sz*sx*sy, sz*cy + cz*sx*sy, -cx * sy},
		yAxis: vector{-sz * cx, cz * cx, sx},
		zAxis: vector{cz*sy + sz*sx*cy, sz*sy - cz*sx*cy, cx * cy},
	}
}

func sincos(t float32) (float32, float32) {
	s, c := math.Sincos(float64(t))
	return float32(s), float32(c)
}

var propColors = [8]color.RGBA{
	{}, {}, {255, 236, 196, 255}, {81, 77, 131, 255}, {233, 172, 83, 255},
	{116, 67, 94, 255}, {255, 213, 103, 255}, {169, 154, 195, 255},
}

type projectedVertex struct{ x, y, z, light, shine float32 }

// Renderer draws the exported mesh in a compact orthographic viewport.
// An instance belongs to one widget and reuses its depth and vertex buffers.
type Renderer struct {
	pixels   *image.RGBA
	depth    []float32
	vertices []projectedVertex
}

func (r *Renderer) Draw(pixels int, shape string, bodyColor, eyeColor color.RGBA, pose Pose, portrait bool) *image.RGBA {
	if r.pixels == nil || r.pixels.Rect.Dx() != pixels {
		r.pixels = image.NewRGBA(image.Rect(0, 0, pixels, pixels))
		r.depth = make([]float32, pixels*pixels)
	}
	clear(r.pixels.Pix)
	for i := range r.depth {
		r.depth[i] = -100
	}
	colors := propColors
	colors[0], colors[1] = bodyColor, eyeColor

	index := shapeIndex(shape)
	v := pose.values
	root := rotation(.145+v[0], .025+v[1], .12+v[2])
	root.location.y = v[3] + .045
	scale := vector{1 + v[4], 1 - v[4], 1 + v[4]*.6}
	framing := float32(2.86)
	if portrait {
		framing = 2.48
	}
	r.part(index, vector{}, scale, 0, root, framing, colors)
	if index == 0 {
		r.part(crest, vector{-.25 * scale.x, .83 * scale.y, .08 * scale.z}, scale, .18, root, framing, colors)
	}
	for i, anchor := range bud.eyes[index] {
		angle := float32(.39 - .07*float32(i))
		position := anchor.add(vector{v[6], v[7], -.30 * v[7]})
		position = vector{position.x * scale.x, position.y * scale.y, position.z * scale.z}
		eyeScale := vector{(1 - .09*float32(i)) * scale.x, (1 - .87*v[5]) * scale.y, scale.z}
		r.part(eye, position, eyeScale, angle, root, framing, colors)
	}
	if pop := v[11]; !portrait && pop > .01 {
		size := vector{pop, pop, pop}
		position := vector{v[8], v[9], v[10]}
		switch pose.Action {
		case Investigate:
			r.part(loupe, position, size, v[12], root, framing, colors)
		case Notebook:
			r.part(book, position, size, v[12], root, framing, colors)
		case Idea:
			r.part(bulb, position, size, v[12], root, framing, colors)
		case Tea:
			r.part(teacup, position, size, v[12], root, framing, colors)
		case Sketch:
			r.part(pencil, position, size, v[12], root, framing, colors)
		case Music:
			r.part(headphones, position, size, v[12], root, framing, colors)
		case Letter:
			r.part(envelope, position, size, v[12], root, framing, colors)
		case Telescope:
			r.part(telescope, position, size, v[12], root, framing, colors)
		}
	}
	return r.pixels
}

func (r *Renderer) part(index int, position, scale vector, roll float32, root transform, framing float32, colors [8]color.RGBA) {
	part := bud.meshes[index]
	if cap(r.vertices) < len(part.vertices) {
		r.vertices = make([]projectedVertex, len(part.vertices))
	}
	r.vertices = r.vertices[:len(part.vertices)]

	dimension := float32(r.pixels.Rect.Dx())
	unit := dimension / framing
	s, c := sincos(roll)
	lightDirection := vector{-.42, .66, .62}
	halfVector := vector{-.22, .35, .91}
	for i, v := range part.vertices {
		x, y, z := v.position.x*scale.x, v.position.y*scale.y, v.position.z*scale.z
		point := root.rotate(position.add(vector{c*x - s*y, s*x + c*y, z})).add(root.location)
		nx, ny, nz := v.normal.x/scale.x, v.normal.y/scale.y, v.normal.z/scale.z
		normal := root.rotate(vector{c*nx - s*ny, s*nx + c*ny, nz})
		length := float32(math.Sqrt(float64(normal.dot(normal))))
		diffuse := max(0, normal.dot(lightDirection)/length)
		specular := max(0, normal.dot(halfVector)/length)
		power := specular * specular
		power *= power
		power *= power
		shine := power * .12
		light := .54 + .42*diffuse + .045*max(0, -normal.y)/length
		if index == eye {
			shine = 0
			light = 1
		}
		r.vertices[i] = projectedVertex{
			dimension*.5 + point.x*unit,
			dimension*.50 - point.y*unit,
			point.z,
			light,
			shine,
		}
	}
	for _, f := range part.faces {
		r.fill(r.vertices[f.a], r.vertices[f.b], r.vertices[f.c], colors[f.material])
	}
}

func (r *Renderer) fill(a, b, c projectedVertex, base color.RGBA) {
	dx1, dy1, dx2, dy2 := b.x-a.x, b.y-a.y, c.x-a.x, c.y-a.y
	area := dx1*dy2 - dy1*dx2
	if abs(area) < .0001 {
		return
	}
	n := r.pixels.Rect.Dx()
	left := max(0, int(math.Floor(float64(min(a.x, b.x, c.x)))))
	top := max(0, int(math.Floor(float64(min(a.y, b.y, c.y)))))
	right := min(n-1, int(max(a.x, b.x, c.x)))
	bottom := min(n-1, int(max(a.y, b.y, c.y)))
	inv := 1 / area
	dbx, dby := dy2*inv, -dx2*inv
	dcx, dcy := -dy1*inv, dx1*inv
	for y := top; y <= bottom; y++ {
		px, py := float32(left)+.5-a.x, float32(y)+.5-a.y
		u, v := px*dbx+py*dby, px*dcx+py*dcy
		i := y*n + left
		for x := left; x <= right; x++ {
			w := 1 - u - v
			if u >= -.0001 && v >= -.0001 && w >= -.0001 {
				depth := a.z*w + b.z*u + c.z*v
				if depth > r.depth[i] {
					r.depth[i] = depth
					light := a.light*w + b.light*u + c.light*v
					shine := 255 * (a.shine*w + b.shine*u + c.shine*v)
					r.pixels.Pix[i*4] = shade(base.R, light, shine)
					r.pixels.Pix[i*4+1] = shade(base.G, light, shine)
					r.pixels.Pix[i*4+2] = shade(base.B, light, shine)
					r.pixels.Pix[i*4+3] = 255
				}
			}
			u, v, i = u+dbx, v+dcx, i+1
		}
	}
}

func shade(channel uint8, light, shine float32) uint8 {
	return uint8(min(255, float32(channel)*light+shine))
}

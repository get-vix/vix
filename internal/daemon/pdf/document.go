package pdf

// page holds the positioned text runs extracted from a single page, plus its
// media box dimensions.
type page struct {
	runs          []textRun
	width, height float64
}

// pages walks the page tree and extracts positioned text runs from each page.
func (d *Document) pages() []page {
	catalog, _ := d.Resolve(d.trailer["Root"]).(Dict)
	if catalog == nil {
		return nil
	}
	root, _ := d.Resolve(catalog["Pages"]).(Dict)
	if root == nil {
		return nil
	}
	var out []page
	seen := map[int]bool{}
	d.walkPageTree(root, inherited{}, seen, &out)
	return out
}

// inherited carries page attributes that descend through the page tree.
type inherited struct {
	resources Dict
	mediaBox  Array
}

func (d *Document) walkPageTree(node Dict, inh inherited, seen map[int]bool, out *[]page) {
	if len(*out) > 5000 {
		return // safety cap on page count
	}
	if res, ok := d.Resolve(node["Resources"]).(Dict); ok {
		inh.resources = res
	}
	if mb, ok := d.Resolve(node["MediaBox"]).(Array); ok {
		inh.mediaBox = mb
	}
	switch name(d.Resolve(node["Type"])) {
	case "Pages", "":
		kids, _ := d.Resolve(node["Kids"]).(Array)
		for _, kid := range kids {
			ref, isRef := kid.(Reference)
			if isRef {
				if seen[ref.Num] {
					continue
				}
				seen[ref.Num] = true
			}
			if child, ok := d.Resolve(kid).(Dict); ok {
				d.walkPageTree(child, inh, seen, out)
			}
		}
	case "Page":
		*out = append(*out, d.extractPage(node, inh))
	}
}

func (d *Document) extractPage(node Dict, inh inherited) page {
	var pg page
	mb := inh.mediaBox
	if len(mb) == 4 {
		x0, _ := Float(d.Resolve(mb[0]))
		y0, _ := Float(d.Resolve(mb[1]))
		x1, _ := Float(d.Resolve(mb[2]))
		y1, _ := Float(d.Resolve(mb[3]))
		pg.width = x1 - x0
		pg.height = y1 - y0
	}
	fonts := d.buildFonts(inh.resources)
	content := d.pageContent(node)
	if len(content) == 0 {
		return pg
	}
	ce := newContentExtractor(d, fonts)
	ce.run(content)
	pg.runs = ce.runs
	return pg
}

// pageContent concatenates a page's content stream(s) into a single decoded
// buffer.
func (d *Document) pageContent(node Dict) []byte {
	c := d.Resolve(node["Contents"])
	var streams []Stream
	switch v := c.(type) {
	case Stream:
		streams = append(streams, v)
	case Array:
		for _, e := range v {
			if st, ok := d.Resolve(e).(Stream); ok {
				streams = append(streams, st)
			}
		}
	}
	var buf []byte
	for _, st := range streams {
		data, err := d.decodeStream(st)
		if err != nil {
			continue
		}
		buf = append(buf, data...)
		buf = append(buf, '\n') // separate streams so operators don't merge
	}
	return buf
}

// buildFonts constructs the font map from a resource dictionary's /Font entry.
func (d *Document) buildFonts(resources Dict) map[Name]*font {
	fonts := map[Name]*font{}
	if resources == nil {
		return fonts
	}
	fdict, _ := d.Resolve(resources["Font"]).(Dict)
	for key, val := range fdict {
		if fd, ok := d.Resolve(val).(Dict); ok {
			fonts[key] = d.buildFont(fd)
		}
	}
	return fonts
}

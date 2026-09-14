package mesh

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/qmuntal/go3mf"
	_ "github.com/qmuntal/go3mf/production"
	"github.com/qmuntal/opc"
)

type metadataReaderAt struct {
	ctx       context.Context
	r         io.ReaderAt
	remaining int64
}

func (r *metadataReaderAt) ReadAt(p []byte, offset int64) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if r.remaining >= 0 {
		if int64(len(p)) > r.remaining {
			return 0, errMeshLimit
		}
		r.remaining -= int64(len(p))
	}
	return r.r.ReadAt(p, offset)
}

type xmlBudget struct {
	vertices, triangles, instances int
}

func checkXML(ctx context.Context, data []byte, limits parserLimits, budget *xmlBudget) error {
	decoder := xml.NewDecoder(contextReader{ctx: ctx, r: bytes.NewReader(data)})
	depth, roots := 0, 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		token, err := decoder.Token()
		if err == io.EOF {
			if roots != 1 {
				return errors.New("invalid 3mf XML document")
			}
			return nil
		}
		if err != nil {
			return err
		}
		switch element := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
				if roots > 1 {
					return errors.New("invalid 3mf XML document")
				}
			}
			depth++
			if depth > limits.depth {
				return errMeshLimit
			}
			switch element.Name.Local {
			case "vertex":
				budget.vertices++
			case "triangle":
				budget.triangles++
			case "component", "object", "item", "Relationship":
				budget.instances++
			}
			if budget.vertices > limits.vertices || budget.triangles > limits.triangles || budget.instances > limits.instances {
				return errMeshLimit
			}
			for _, attr := range element.Attr {
				if attr.Name.Local == "transform" || (element.Name.Local == "vertex" && (attr.Name.Local == "x" || attr.Name.Local == "y" || attr.Name.Local == "z")) {
					for _, number := range strings.Fields(attr.Value) {
						value, err := strconv.ParseFloat(number, 32)
						if err != nil || !finite(value) {
							return errors.New("invalid 3mf coordinates or transform")
						}
					}
				}
			}
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && len(bytes.TrimSpace(element)) != 0 {
				return errors.New("invalid 3mf XML document")
			}
		}
	}
}

func decode3MF(ctx context.Context, r io.ReaderAt, size int64, limits parserLimits) (*go3mf.Model, error) {
	reader := &metadataReaderAt{ctx: ctx, r: r, remaining: 4 << 20}
	archive, err := zip.NewReader(reader, size)
	if err != nil {
		return nil, err
	}
	if len(archive.File) > limits.entries {
		return nil, errMeshLimit
	}
	reader.remaining = -1
	var expanded int64
	parts := make(map[string][]byte, len(archive.File))
	seen := make(map[string]bool, len(archive.File))
	for _, entry := range archive.File {
		name := entry.Name
		if entry.FileInfo().IsDir() {
			continue
		}
		if name == "" || strings.ContainsAny(name, "\\\x00") || strings.HasPrefix(name, "/") || path.Clean(name) != name || strings.HasPrefix(name, "../") || seen[strings.ToLower(name)] {
			return nil, errors.New("invalid 3mf package path")
		}
		seen[strings.ToLower(name)] = true
		if entry.UncompressedSize64 > uint64(limits.expandedBytes-expanded) {
			return nil, errMeshLimit
		}
		stream, err := entry.Open()
		if err != nil {
			return nil, err
		}
		// Entry reads must also fit the budget when ZIP metadata is forged.
		data, readErr := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, r: stream}, limits.expandedBytes-expanded+1))
		closeErr := stream.Close()
		if readErr != nil || closeErr != nil {
			return nil, errors.Join(readErr, closeErr)
		}
		expanded += int64(len(data))
		if expanded > limits.expandedBytes {
			return nil, errMeshLimit
		}
		parts["/"+name] = data
	}
	if _, ok := parts["/[Content_Types].xml"]; !ok {
		return nil, errors.New("3mf package has no content types")
	}
	var budget xmlBudget
	checked := make(map[string]bool)
	models := make(map[string]bool)
	root := ""
	for name, data := range parts {
		if strings.HasSuffix(strings.ToLower(name), ".xml") || strings.HasSuffix(strings.ToLower(name), ".rels") {
			if err := checkXML(ctx, data, limits, &budget); err != nil {
				return nil, err
			}
			checked[name] = true
		}
		if !strings.HasSuffix(name, ".rels") {
			continue
		}
		var rels struct {
			XMLName xml.Name `xml:"http://schemas.openxmlformats.org/package/2006/relationships Relationships"`
			Entries []struct {
				Type   string `xml:"Type,attr"`
				Target string `xml:"Target,attr"`
				Mode   string `xml:"TargetMode,attr"`
			} `xml:"Relationship"`
		}
		if err := xml.Unmarshal(data, &rels); err != nil {
			return nil, err
		}
		source := path.Join(path.Dir(path.Dir(name)), strings.TrimSuffix(path.Base(name), ".rels"))
		if name == "/_rels/.rels" {
			source = "/"
		}
		for _, rel := range rels.Entries {
			if rel.Type != go3mf.RelType3DModel {
				continue
			}
			if rel.Mode == "External" {
				return nil, errors.New("external 3mf models are unsupported")
			}
			target := opc.ResolveRelationship(source, rel.Target)
			if _, ok := parts[target]; !ok {
				return nil, errors.New("3mf model part is missing")
			}
			models[target] = true
			if source == "/" {
				if root != "" {
					return nil, errors.New("3mf package has multiple root models")
				}
				root = target
			}
		}
	}
	if root == "" {
		return nil, errors.New("3mf package has no root model")
	}
	var types struct {
		XMLName  xml.Name `xml:"http://schemas.openxmlformats.org/package/2006/content-types Types"`
		Defaults []struct {
			Extension string `xml:"Extension,attr"`
			Type      string `xml:"ContentType,attr"`
		} `xml:"Default"`
		Overrides []struct {
			Name string `xml:"PartName,attr"`
			Type string `xml:"ContentType,attr"`
		} `xml:"Override"`
	}
	if err := xml.Unmarshal(parts["/[Content_Types].xml"], &types); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(models))
	for name := range models {
		contentType := ""
		for _, entry := range types.Defaults {
			if strings.EqualFold(entry.Extension, strings.TrimPrefix(path.Ext(name), ".")) {
				contentType = entry.Type
			}
		}
		for _, entry := range types.Overrides {
			if strings.EqualFold(entry.Name, name) {
				contentType = entry.Type
			}
		}
		if contentType != "application/vnd.ms-package.3dmanufacturing-3dmodel+xml" {
			return nil, errors.New("invalid 3mf model content type")
		}
		if !checked[name] {
			if err := checkXML(ctx, parts[name], limits, &budget); err != nil {
				return nil, err
			}
		}
		names = append(names, name)
	}
	sort.Strings(names)
	var model *go3mf.Model
	children := make(map[string]*go3mf.ChildModel)
	for _, name := range names {
		decoded, err := decodeModelPart(ctx, parts[name])
		if err != nil {
			return nil, err
		}
		if name == root {
			model = decoded
		} else {
			children[name] = &go3mf.ChildModel{Resources: decoded.Resources}
		}
	}
	model.Path, model.Childs = root, children
	return model, nil
}

func decodeModelPart(ctx context.Context, data []byte) (*go3mf.Model, error) {
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	parts := []struct {
		name string
		data []byte
	}{
		{"[Content_Types].xml", []byte(`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="model" ContentType="application/vnd.ms-package.3dmanufacturing-3dmodel+xml"/></Types>`)},
		{"_rels/.rels", []byte(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="root" Type="http://schemas.microsoft.com/3dmanufacturing/2013/01/3dmodel" Target="/3D/3dmodel.model"/></Relationships>`)},
		{"3D/3dmodel.model", data},
	}
	for _, part := range parts {
		writer, err := archive.CreateHeader(&zip.FileHeader{Name: part.name, Method: zip.Store})
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(writer, contextReader{ctx: ctx, r: bytes.NewReader(part.data)}); err != nil {
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	// Decode parts serially; upstream starts a goroutine for every linked model.
	model := new(go3mf.Model)
	if err := go3mf.NewDecoder(bytes.NewReader(buffer.Bytes()), int64(buffer.Len())).DecodeContext(ctx, model); err != nil {
		return nil, err
	}
	return model, nil
}

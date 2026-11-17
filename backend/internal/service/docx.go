package service

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"strings"
)

const docxContentType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

func textToDocx(text string) ([]byte, error) {
	var body strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			body.WriteString("<w:p/>")
			continue
		}
		var escaped bytes.Buffer
		if err := xml.EscapeText(&escaped, []byte(line)); err != nil {
			return nil, err
		}
		body.WriteString(`<w:p><w:r><w:t xml:space="preserve">`)
		body.Write(escaped.Bytes())
		body.WriteString(`</w:t></w:r></w:p>`)
	}

	parts := []struct {
		name string
		body string
	}{
		{
			name: "[Content_Types].xml",
			body: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
				`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
				`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
				`<Default Extension="xml" ContentType="application/xml"/>` +
				`<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>` +
				`</Types>`,
		},
		{
			name: "_rels/.rels",
			body: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
				`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
				`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>` +
				`</Relationships>`,
		},
		{
			name: "word/document.xml",
			body: `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
				`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` +
				body.String() +
				`<w:sectPr><w:pgSz w:w="12240" w:h="15840"/>` +
				`<w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440" w:header="720" w:footer="720" w:gutter="0"/>` +
				`</w:sectPr></w:body></w:document>`,
		},
	}

	var output bytes.Buffer
	archive := zip.NewWriter(&output)
	for _, part := range parts {
		writer, err := archive.Create(part.name)
		if err != nil {
			_ = archive.Close()
			return nil, err
		}
		if _, err := io.WriteString(writer, part.body); err != nil {
			_ = archive.Close()
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

package sfntest

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var csvDelimiters = map[string]byte{
	"":          ',',
	"COMMA":     ',',
	"PIPE":      '|',
	"SEMICOLON": ';',
	"SPACE":     ' ',
	"TAB":       '\t',
}

// readItem is an item read by an ItemReader.
type readItem struct {
	value  any
	key    string // set for items of a JSON object
	hasKey bool
	source string
}

func itemReaderFailed(format string, args ...any) *StatesError {
	return statesErrorf(ErrorItemReaderFailed, format, args...)
}

// readItems reads the items of a distributed Map from S3 through the API.
func (r *runner) readItems(rd *itemReader, params any, ev *Event) ([]readItem, error) {
	cfg := rd.ReaderConfig
	if cfg == nil {
		cfg = &readerConfig{}
	}
	p, ok := params.(map[string]any)
	if !ok {
		return nil, itemReaderFailed("ItemReader parameters must be an object, got %s", typeName(params))
	}
	bucket, _ := p["Bucket"].(string)

	if rd.Resource == resourceListObjectsV2 {
		objects, err := r.listObjects(p, ev)
		if err != nil {
			return nil, err
		}
		if cfg.Transformation != "LOAD_AND_FLATTEN" {
			items := make([]readItem, len(objects))
			for i, o := range objects {
				items[i] = readItem{value: o, source: "S3://" + bucket}
			}
			return items, nil
		}

		var items []readItem
		for _, o := range objects {
			key, _ := o.(map[string]any)["Key"].(string)
			if strings.HasSuffix(key, "/") {
				continue
			}
			got, err := r.readObject(bucket, key, cfg.InputType, cfg, ev)
			if err != nil {
				return nil, err
			}
			items = append(items, got...)
		}
		return items, nil
	}

	key, _ := p["Key"].(string)
	switch {
	case cfg.ManifestType == "S3_INVENTORY":
		return r.readInventory(p, bucket, key, cfg, ev)
	case cfg.ManifestType == "ATHENA_DATA" || cfg.InputType == "MANIFEST":
		return r.readAthenaManifest(p, cfg, ev)
	}

	b, err := r.getObject(p, ev)
	if err != nil {
		return nil, err
	}
	return parseObject(b, cfg.InputType, cfg, "S3://"+bucket+"/"+key)
}

func (r *runner) readObject(bucket, key, inputType string, cfg *readerConfig, ev *Event) ([]readItem, error) {
	b, err := r.getObject(map[string]any{"Bucket": bucket, "Key": key}, ev)
	if err != nil {
		return nil, err
	}
	return parseObject(b, inputType, cfg, "S3://"+bucket+"/"+key)
}

func (r *runner) getObject(params map[string]any, ev *Event) ([]byte, error) {
	b, err := r.callRaw(resourceGetObject, params, ev, ErrorItemReaderFailed)
	if err != nil {
		return nil, err
	}
	return decompress(b)
}

func decompress(b []byte) ([]byte, error) {
	switch {
	case bytes.HasPrefix(b, []byte{0x1f, 0x8b}):
		zr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return nil, itemReaderFailed("invalid gzip data: %s", err)
		}
		out, err := io.ReadAll(zr)
		if err != nil {
			return nil, itemReaderFailed("invalid gzip data: %s", err)
		}
		return out, nil
	case bytes.HasPrefix(b, []byte{0x28, 0xb5, 0x2f, 0xfd}):
		return nil, itemReaderFailed("ZSTD compressed objects are not supported")
	}
	return b, nil
}

func parseObject(b []byte, inputType string, cfg *readerConfig, source string) ([]readItem, error) {
	var values []any
	var keys []string
	var err error

	switch inputType {
	case "", "JSON":
		values, keys, err = parseJSONItems(b, cfg.ItemsPointer)
	case "JSONL":
		values, err = parseJSONL(b)
	case "CSV":
		var rows [][]string
		if rows, err = parseCSV(b, csvDelimiters[cfg.CSVDelimiter]); err == nil {
			values = csvItems(rows, cfg.CSVHeaderLocation == "GIVEN", cfg.CSVHeaders)
		}
	default:
		return nil, itemReaderFailed("unsupported InputType: %s", inputType)
	}
	if err != nil {
		return nil, itemReaderFailed("%s: %s", source, err)
	}

	items := make([]readItem, len(values))
	for i, v := range values {
		items[i] = readItem{value: v, source: source}
		if keys != nil {
			items[i].key, items[i].hasKey = keys[i], true
		}
	}
	return items, nil
}

func parseJSONItems(b []byte, pointer string) ([]any, []string, error) {
	v, err := decodeJSON(b)
	if err != nil {
		return nil, nil, err
	}
	if pointer != "" {
		if v, err = jsonPointer(v, pointer); err != nil {
			return nil, nil, err
		}
	}
	switch t := v.(type) {
	case []any:
		return t, nil, nil
	case map[string]any:
		keys := sortedKeys(t)
		values := make([]any, len(keys))
		for i, k := range keys {
			values[i] = t[k]
		}
		return values, keys, nil
	}
	return nil, nil, fmt.Errorf("the JSON must be an array or an object, got %s", typeName(v))
}

// jsonPointer resolves an RFC 6901 JSON Pointer.
func jsonPointer(v any, pointer string) (any, error) {
	if !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("invalid ItemsPointer: %s", pointer)
	}
	for _, tok := range strings.Split(pointer[1:], "/") {
		tok = strings.NewReplacer("~1", "/", "~0", "~").Replace(tok)
		switch t := v.(type) {
		case map[string]any:
			c, ok := t[tok]
			if !ok {
				return nil, fmt.Errorf("ItemsPointer %s: %q not found", pointer, tok)
			}
			v = c
		case []any:
			i, err := strconv.Atoi(tok)
			if err != nil || i < 0 || i >= len(t) {
				return nil, fmt.Errorf("ItemsPointer %s: invalid index %q", pointer, tok)
			}
			v = t[i]
		default:
			return nil, fmt.Errorf("ItemsPointer %s: cannot descend into %s", pointer, typeName(v))
		}
	}
	return v, nil
}

func parseJSONL(b []byte) ([]any, error) {
	var out []any
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		v, err := decodeJSON([]byte(line))
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		out = append(out, v)
	}
	return out, sc.Err()
}

// parseCSV parses CSV as Step Functions does: quotes are optional, a quoted
// field may contain the delimiter and newlines, "" is a literal quote, and a
// backslash escapes a backslash, a quote or the delimiter (before any other
// character it is removed).
func parseCSV(b []byte, delim byte) ([][]string, error) {
	var rows [][]string
	var row []string
	var field strings.Builder
	inQuotes := false
	fieldStarted := false

	endField := func() {
		row = append(row, field.String())
		field.Reset()
		fieldStarted = false
	}
	endRow := func() {
		endField()
		if len(row) != 1 || row[0] != "" {
			rows = append(rows, row)
		}
		row = nil
	}

	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case c == '\\':
			if i+1 < len(b) && (b[i+1] == '\\' || b[i+1] == '"' || b[i+1] == delim) {
				i++
				field.WriteByte(b[i])
			}
			fieldStarted = true
		case inQuotes:
			if c == '"' {
				if i+1 < len(b) && b[i+1] == '"' {
					field.WriteByte('"')
					i++
				} else {
					inQuotes = false
				}
			} else {
				field.WriteByte(c)
			}
		case c == '"' && !fieldStarted:
			inQuotes = true
			fieldStarted = true
		case c == delim:
			endField()
		case c == '\r' && i+1 < len(b) && b[i+1] == '\n':
		case c == '\n':
			endRow()
		default:
			field.WriteByte(c)
			fieldStarted = true
		}
	}
	if inQuotes {
		return nil, fmt.Errorf("unclosed quote")
	}
	if field.Len() > 0 || len(row) > 0 {
		endRow()
	}
	return rows, nil
}

func csvItems(rows [][]string, given bool, headers []string) []any {
	if !given {
		if len(rows) == 0 {
			return nil
		}
		headers, rows = rows[0], rows[1:]
	}
	items := make([]any, 0, len(rows))
	for _, row := range rows {
		item := make(map[string]any, len(headers))
		for i, h := range headers {
			if i < len(row) {
				item[h] = row[i]
			} else {
				item[h] = ""
			}
		}
		items = append(items, item)
	}
	return items
}

// listObjects calls ListObjectsV2 until all pages are read and returns the
// items as Step Functions gives them to the Map.
func (r *runner) listObjects(params map[string]any, ev *Event) ([]any, error) {
	var items []any
	token := ""
	for {
		p := make(map[string]any, len(params)+1)
		for k, v := range params {
			p[k] = v
		}
		if token != "" {
			p["ContinuationToken"] = token
		}

		b, err := r.callRaw(resourceListObjectsV2, p, ev, ErrorItemReaderFailed)
		if err != nil {
			return nil, err
		}
		v, err := decodeJSON(b)
		if err != nil {
			return nil, itemReaderFailed("ListObjectsV2 returned invalid JSON: %s", err)
		}
		resp, ok := v.(map[string]any)
		if !ok {
			return nil, itemReaderFailed("ListObjectsV2 must return an object, got %s", typeName(v))
		}

		contents, _ := resp["Contents"].([]any)
		for _, c := range contents {
			o, ok := c.(map[string]any)
			if !ok {
				return nil, itemReaderFailed("ListObjectsV2 Contents must be objects")
			}
			item, err := listItem(o)
			if err != nil {
				return nil, err
			}
			items = append(items, item)
		}

		truncated, _ := resp["IsTruncated"].(bool)
		token, _ = resp["NextContinuationToken"].(string)
		if !truncated || token == "" {
			return items, nil
		}
	}
}

// listItem converts an S3 object of a ListObjectsV2 response into a Map item:
// {"Etag", "Key", "LastModified" (epoch seconds), "Size", "StorageClass"}.
func listItem(o map[string]any) (map[string]any, error) {
	item := map[string]any{"Key": o["Key"]}
	if v, ok := o["ETag"]; ok {
		item["Etag"] = v
	} else if v, ok := o["Etag"]; ok {
		item["Etag"] = v
	}
	if v, ok := o["Size"]; ok {
		item["Size"] = v
	}
	if v, ok := o["StorageClass"]; ok {
		item["StorageClass"] = v
	}
	switch lm := o["LastModified"].(type) {
	case nil:
	case json.Number:
		item["LastModified"] = lm
	case string:
		t, err := time.Parse(time.RFC3339Nano, lm)
		if err != nil {
			return nil, itemReaderFailed("invalid LastModified %q: %s", lm, err)
		}
		item["LastModified"] = mustNormalize(t.Unix())
	default:
		return nil, itemReaderFailed("invalid LastModified: %s", typeName(lm))
	}
	return item, nil
}

// readInventory reads an S3 Inventory manifest.json and its CSV data files.
func (r *runner) readInventory(params map[string]any, bucket, key string, cfg *readerConfig, ev *Event) ([]readItem, error) {
	b, err := r.getObject(params, ev)
	if err != nil {
		return nil, err
	}
	var manifest struct {
		DestinationBucket string `json:"destinationBucket"`
		FileSchema        string `json:"fileSchema"`
		Files             []struct {
			Key string `json:"key"`
		} `json:"files"`
	}
	if err := json.Unmarshal(b, &manifest); err != nil {
		return nil, itemReaderFailed("invalid S3 Inventory manifest %s: %s", key, err)
	}

	headers := strings.Split(manifest.FileSchema, ",")
	for i := range headers {
		headers[i] = strings.TrimSpace(headers[i])
	}
	dataBucket := bucket
	if arn, ok := strings.CutPrefix(manifest.DestinationBucket, "arn:aws:s3:::"); ok {
		dataBucket = arn
	}

	var items []readItem
	for _, f := range manifest.Files {
		data, err := r.getObject(map[string]any{"Bucket": dataBucket, "Key": f.Key}, ev)
		if err != nil {
			return nil, err
		}
		rows, err := parseCSV(data, csvDelimiters[cfg.CSVDelimiter])
		if err != nil {
			return nil, itemReaderFailed("%s: %s", f.Key, err)
		}
		for _, v := range csvItems(rows, true, headers) {
			items = append(items, readItem{value: v, source: "S3://" + dataBucket + "/" + f.Key})
		}
	}
	return items, nil
}

// readAthenaManifest reads an Athena UNLOAD manifest (one S3 URI per line)
// and the data files it lists.
func (r *runner) readAthenaManifest(params map[string]any, cfg *readerConfig, ev *Event) ([]readItem, error) {
	b, err := r.getObject(params, ev)
	if err != nil {
		return nil, err
	}
	inputType := cfg.InputType
	if inputType == "" || inputType == "MANIFEST" {
		inputType = "CSV"
	}

	var items []readItem
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		u, err := url.Parse(line)
		if err != nil || !strings.EqualFold(u.Scheme, "s3") {
			return nil, itemReaderFailed("invalid S3 URI in the manifest: %s", line)
		}
		got, err := r.readObject(u.Host, strings.TrimPrefix(u.Path, "/"), inputType, cfg, ev)
		if err != nil {
			return nil, err
		}
		items = append(items, got...)
	}
	return items, nil
}

package sfntest_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/winebarrel/sfntest"
)

// s3Mock serves getObject from files and records putObject calls.
type s3Mock struct {
	files map[string][]byte // "bucket/key" -> body
	lists []string          // ListObjectsV2 pages (JSON)
	puts  map[string]string // "bucket/key" -> body
	calls []string
}

func (m *s3Mock) Call(_ context.Context, resource string, params []byte) ([]byte, error) {
	var p map[string]any
	_ = json.Unmarshal(params, &p)
	m.calls = append(m.calls, resource)

	switch resource {
	case "arn:aws:states:::s3:getObject":
		b, ok := m.files[fmt.Sprintf("%s/%s", p["Bucket"], p["Key"])]
		if !ok {
			return nil, sfntest.Error("S3.NoSuchKey", fmt.Sprint(p["Key"]))
		}
		return b, nil
	case "arn:aws:states:::s3:listObjectsV2":
		i := 0
		if tok, ok := p["ContinuationToken"].(string); ok {
			fmt.Sscan(tok, &i)
		}
		return []byte(m.lists[i]), nil
	case "arn:aws:states:::s3:putObject":
		if m.puts == nil {
			m.puts = map[string]string{}
		}
		m.puts[fmt.Sprintf("%s/%s", p["Bucket"], p["Key"])] = p["Body"].(string)
		return []byte(`{}`), nil
	}
	return []byte(`"processed"`), nil
}

const echoProcessor = `"ItemProcessor":{"ProcessorConfig":{"Mode":"DISTRIBUTED","ExecutionType":"STANDARD"},
  "StartAt":"Echo","States":{"Echo":{"Type":"Pass","End":true}}}`

func dmapDef(fields string) string {
	return `{"StartAt":"M","States":{"M":{"Type":"Map",` + fields + `,` + echoProcessor + `,"End":true}}}`
}

func TestDistributedMapStateData(t *testing.T) {
	def := `{"StartAt":"M","States":{"M":{"Type":"Map","ItemsPath":"$.items",
	  "ItemSelector":{"i.$":"$$.Map.Item.Index","v.$":"$$.Map.Item.Value","src.$":"$$.Map.Item.Source"},
	  "Label":"lbl",
	  "ItemProcessor":{"ProcessorConfig":{"Mode":"DISTRIBUTED","ExecutionType":"STANDARD"},
	    "StartAt":"P","States":{"P":{"Type":"Pass","Parameters":{
	      "in.$":"$","id.$":"$$.Execution.Id","sm.$":"$$.StateMachine.Name"},"End":true}}},
	  "End":true}}}`
	env := &sfntest.Env{Execution: &sfntest.Execution{StateMachineArn: "arn:aws:states:us-east-1:111122223333:stateMachine:parent"}}

	res := run(t, def, `{"items":["a","b"]}`, env)
	require.Equal(t, sfntest.Succeeded, res.Status, res.Cause)

	var out []map[string]any
	require.NoError(t, json.Unmarshal(res.Output, &out))
	require.Len(t, out, 2)
	assert.Equal(t, map[string]any{"i": float64(1), "v": "b", "src": "STATE_DATA"}, out[1]["in"])
	assert.Regexp(t, `^arn:aws:states:us-east-1:111122223333:execution:parent/lbl:[0-9a-f-]{36}$`, out[0]["id"])
	assert.Equal(t, "parent/lbl", out[0]["sm"])
	assert.Len(t, res.History[0].Branches, 2)
}

func TestDistributedMapNoOuterVariables(t *testing.T) {
	def := `{"StartAt":"A","States":{
	  "A":{"Type":"Pass","Assign":{"x":1},"Next":"M"},
	  "M":{"Type":"Map","ItemProcessor":{"ProcessorConfig":{"Mode":"DISTRIBUTED","ExecutionType":"STANDARD"},
	    "StartAt":"P","States":{"P":{"Type":"Pass","Parameters":{"x.$":"$x"},"End":true}}},"End":true}}}`
	res := run(t, def, `[1]`, nil)
	assert.Equal(t, sfntest.Failed, res.Status)
	assert.Equal(t, "States.ExceedToleratedFailureThreshold", res.Error)
}

func TestDistributedMapCSV(t *testing.T) {
	csv := "id,name,note\n1,alice,\"has, comma\"\n2,bob\n3,\"say \"\"hi\"\"\",x,extra\n4,dave,y\n"
	m := &s3Mock{files: map[string][]byte{"bkt/data.csv": []byte(csv)}}
	def := dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:getObject",
	  "ReaderConfig":{"InputType":"CSV","CSVHeaderLocation":"FIRST_ROW","MaxItems":3},
	  "Parameters":{"Bucket":"bkt","Key.$":"$.key"}}`)

	res := run(t, def, `{"key":"data.csv"}`, &sfntest.Env{API: m})
	require.Equal(t, sfntest.Succeeded, res.Status, res.Cause)
	assert.JSONEq(t, `[
	  {"id":"1","name":"alice","note":"has, comma"},
	  {"id":"2","name":"bob","note":""},
	  {"id":"3","name":"say \"hi\"","note":"x"}
	]`, string(res.Output))
}

func TestDistributedMapCSVGivenHeaders(t *testing.T) {
	m := &s3Mock{files: map[string][]byte{"b/k": []byte("1|a\n2|b\n")}}
	def := dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:getObject",
	  "ReaderConfig":{"InputType":"CSV","CSVHeaderLocation":"GIVEN","CSVHeaders":["n","s"],"CSVDelimiter":"PIPE"},
	  "Parameters":{"Bucket":"b","Key":"k"}}`)
	res := run(t, def, `{}`, &sfntest.Env{API: m})
	assert.JSONEq(t, `[{"n":"1","s":"a"},{"n":"2","s":"b"}]`, string(res.Output))
}

func gz(t *testing.T, s string) []byte {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, err := w.Write([]byte(s))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return b.Bytes()
}

func TestDistributedMapJSONLAndJSON(t *testing.T) {
	m := &s3Mock{files: map[string][]byte{
		"b/a.jsonl.gz": gz(t, "{\"a\":1}\n\n{\"a\":2}\n"),
		"b/o.json":     []byte(`{"data":{"items":{"x":1,"y":2}}}`),
	}}

	def := dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:getObject",
	  "ReaderConfig":{"InputType":"JSONL"},"Parameters":{"Bucket":"b","Key":"a.jsonl.gz"}}`)
	res := run(t, def, `{}`, &sfntest.Env{API: m})
	assert.JSONEq(t, `[{"a":1},{"a":2}]`, string(res.Output))

	def = dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:getObject",
	  "ReaderConfig":{"InputType":"JSON","ItemsPointer":"/data/items"},"Parameters":{"Bucket":"b","Key":"o.json"}},
	  "ItemSelector":{"k.$":"$$.Map.Item.Key","v.$":"$$.Map.Item.Value","src.$":"$$.Map.Item.Source"}`)
	res = run(t, def, `{}`, &sfntest.Env{API: m})
	assert.JSONEq(t, `[{"k":"x","v":1,"src":"S3://b/o.json"},{"k":"y","v":2,"src":"S3://b/o.json"}]`, string(res.Output))
}

func TestDistributedMapItemReaderFailed(t *testing.T) {
	m := &s3Mock{}
	def := dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:getObject","Parameters":{"Bucket":"b","Key":"missing"}}`)
	res := run(t, def, `{}`, &sfntest.Env{API: m})
	assert.Equal(t, "States.ItemReaderFailed", res.Error)
	assert.Contains(t, res.Cause, "S3.NoSuchKey")
}

func TestDistributedMapListObjects(t *testing.T) {
	m := &s3Mock{lists: []string{
		`{"Contents":[{"Key":"p/a","Size":1,"ETag":"\"e1\"","LastModified":"2022-11-17T15:44:41Z","StorageClass":"STANDARD"}],"IsTruncated":true,"NextContinuationToken":"1"}`,
		`{"Contents":[{"Key":"p/b","Size":2,"ETag":"\"e2\"","LastModified":"2022-11-17T15:44:42Z","StorageClass":"STANDARD"}],"IsTruncated":false}`,
	}}
	def := dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:listObjectsV2","Parameters":{"Bucket":"b","Prefix":"p/"}},
	  "ItemSelector":{"v.$":"$$.Map.Item.Value","src.$":"$$.Map.Item.Source"}`)
	res := run(t, def, `{}`, &sfntest.Env{API: m})
	require.Equal(t, sfntest.Succeeded, res.Status, res.Cause)
	assert.JSONEq(t, `[
	  {"v":{"Key":"p/a","Size":1,"Etag":"\"e1\"","LastModified":1668699881,"StorageClass":"STANDARD"},"src":"S3://b"},
	  {"v":{"Key":"p/b","Size":2,"Etag":"\"e2\"","LastModified":1668699882,"StorageClass":"STANDARD"},"src":"S3://b"}
	]`, string(res.Output))
}

func TestDistributedMapLoadAndFlatten(t *testing.T) {
	m := &s3Mock{
		lists: []string{`{"Contents":[{"Key":"p/"},{"Key":"p/1.jsonl"},{"Key":"p/2.jsonl"}]}`},
		files: map[string][]byte{"b/p/1.jsonl": []byte("1\n2\n"), "b/p/2.jsonl": []byte("3\n")},
	}
	def := dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:listObjectsV2",
	  "ReaderConfig":{"InputType":"JSONL","Transformation":"LOAD_AND_FLATTEN"},"Parameters":{"Bucket":"b","Prefix":"p/"}}`)
	res := run(t, def, `{}`, &sfntest.Env{API: m})
	assert.JSONEq(t, `[1,2,3]`, string(res.Output))
}

func TestDistributedMapInventory(t *testing.T) {
	manifest := `{"sourceBucket":"src","destinationBucket":"arn:aws:s3:::inv","fileSchema":"Bucket, Key, Size, LastModifiedDate",
	  "files":[{"key":"data/1.csv.gz"}]}`
	m := &s3Mock{files: map[string][]byte{
		"inv/m/manifest.json": []byte(manifest),
		"inv/data/1.csv.gz":   gz(t, "src,a.csv,10,2022-11-16T00:29:32.000Z\n"),
	}}
	def := dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:getObject",
	  "ReaderConfig":{"ManifestType":"S3_INVENTORY"},"Parameters":{"Bucket":"inv","Key":"m/manifest.json"}}`)
	res := run(t, def, `{}`, &sfntest.Env{API: m})
	require.Equal(t, sfntest.Succeeded, res.Status, res.Cause)
	assert.JSONEq(t, `[{"Bucket":"src","Key":"a.csv","Size":"10","LastModifiedDate":"2022-11-16T00:29:32.000Z"}]`, string(res.Output))
}

func TestDistributedMapItemBatcher(t *testing.T) {
	def := dmapDef(`"ItemBatcher":{"MaxItemsPerBatch":2,"BatchInput":{"job.$":"$.job"}},"ItemsPath":"$.items"`)
	res := run(t, def, `{"job":"j","items":[1,2,3]}`, nil)
	assert.JSONEq(t, `[{"Items":[1,2],"BatchInput":{"job":"j"}},{"Items":[3],"BatchInput":{"job":"j"}}]`, string(res.Output))

	def = dmapDef(`"ItemBatcher":{"MaxInputBytesPerBatch":20}`)
	res = run(t, def, `["aaaa","bbbb","cccc"]`, nil)
	assert.JSONEq(t, `[{"Items":["aaaa"]},{"Items":["bbbb"]},{"Items":["cccc"]}]`, string(res.Output))
}

const failOddProcessor = `"ItemProcessor":{"ProcessorConfig":{"Mode":"DISTRIBUTED","ExecutionType":"EXPRESS"},
  "StartAt":"C","States":{
    "C":{"Type":"Choice","Choices":[{"Variable":"$","NumericEquals":1,"Next":"F"}],"Default":"S"},
    "F":{"Type":"Fail","Error":"Odd","Cause":"one"},
    "S":{"Type":"Pass","End":true}}}`

func TestDistributedMapToleratedFailure(t *testing.T) {
	def := `{"StartAt":"M","States":{"M":{"Type":"Map","ToleratedFailurePercentage":50,` + failOddProcessor + `,"End":true}}}`
	res := run(t, def, `[0,1,2]`, nil)
	require.Equal(t, sfntest.Succeeded, res.Status, res.Cause)
	assert.JSONEq(t, `[0,{"Error":"Odd","Cause":"one"},2]`, string(res.Output))

	def = `{"StartAt":"M","States":{"M":{"Type":"Map","ToleratedFailureCount":0,` + failOddProcessor + `,"End":true}}}`
	res = run(t, def, `[0,1,2]`, nil)
	assert.Equal(t, "States.ExceedToleratedFailureThreshold", res.Error)
}

func TestDistributedMapResultWriter(t *testing.T) {
	m := &s3Mock{}
	def := `{"StartAt":"M","States":{"M":{"Type":"Map","ToleratedFailureCount":1,
	  "ResultWriter":{"Resource":"arn:aws:states:::s3:putObject","Parameters":{"Bucket":"out","Prefix":"res"},
	    "WriterConfig":{"OutputType":"JSONL","Transformation":"COMPACT"}},` + failOddProcessor + `,"End":true}}}`
	res := run(t, def, `[0,1]`, &sfntest.Env{API: m})
	require.Equal(t, sfntest.Succeeded, res.Status, res.Cause)

	var out struct {
		MapRunArn           string
		ResultWriterDetails struct{ Bucket, Key string }
	}
	require.NoError(t, json.Unmarshal(res.Output, &out))
	assert.Regexp(t, `^arn:aws:states:us-east-1:123456789012:mapRun:test/M:[0-9a-f-]{36}$`, out.MapRunArn)
	assert.Equal(t, "out", out.ResultWriterDetails.Bucket)
	dir := strings.TrimSuffix(out.ResultWriterDetails.Key, "manifest.json")
	assert.True(t, strings.HasPrefix(dir, "res/"))

	assert.Equal(t, "0\n", m.puts["out/"+dir+"SUCCEEDED_0.json"])
	failed := m.puts["out/"+dir+"FAILED_0.json"]
	assert.Contains(t, failed, `"Status":"FAILED"`)
	assert.Contains(t, failed, `"Error":"Odd"`)

	var manifest map[string]any
	require.NoError(t, json.Unmarshal([]byte(m.puts["out/"+out.ResultWriterDetails.Key]), &manifest))
	assert.Equal(t, out.MapRunArn, manifest["MapRunArn"])
}

func TestDistributedMapWriterConfigOnly(t *testing.T) {
	def := dmapDef(`"ResultWriter":{"WriterConfig":{"Transformation":"FLATTEN"}}`)
	res := run(t, def, `[[1,2],[3]]`, nil)
	assert.JSONEq(t, `[1,2,3]`, string(res.Output))

	def = dmapDef(`"ResultWriter":{"WriterConfig":{"Transformation":"NONE"}}`)
	res = run(t, def, `["a"]`, nil)
	var out []map[string]any
	require.NoError(t, json.Unmarshal(res.Output, &out))
	assert.Equal(t, "SUCCEEDED", out[0]["Status"])
	assert.Equal(t, `"a"`, out[0]["Output"])
	assert.Equal(t, `"a"`, out[0]["Input"])
}

func TestDistributedMapJSONata(t *testing.T) {
	m := &s3Mock{files: map[string][]byte{"b/k.json": []byte(`[{"n":1},{"n":2}]`)}}
	def := `{"QueryLanguage":"JSONata","StartAt":"M","States":{"M":{"Type":"Map",
	  "ItemReader":{"Resource":"arn:aws:states:::s3:getObject","Arguments":{"Bucket":"b","Key":"{% $states.input.key %}"}},
	  "ItemSelector":{"n":"{% $states.context.Map.Item.Value.n * 10 %}"},
	  "ToleratedFailureCount":"{% 0 %}",
	  "ItemProcessor":{"ProcessorConfig":{"Mode":"DISTRIBUTED","ExecutionType":"STANDARD"},
	    "StartAt":"P","States":{"P":{"Type":"Pass","Output":"{% $states.input.n %}","End":true}}},
	  "Output":{"sum":"{% $sum($states.result) %}"},"End":true}}}`
	res := run(t, def, `{"key":"k.json"}`, &sfntest.Env{API: m})
	require.Equal(t, sfntest.Succeeded, res.Status, res.Cause)
	assert.JSONEq(t, `{"sum":30}`, string(res.Output))
}

func TestDistributedMapValidation(t *testing.T) {
	cases := []struct {
		def  string
		opts []sfntest.Option
		want string
	}{
		{dmapDef(`"ItemsPath":"$"`), []sfntest.Option{sfntest.WithType(sfntest.Express)}, "not supported in Express workflows"},
		{`{"StartAt":"M","States":{"M":{"Type":"Map","ItemProcessor":{"ProcessorConfig":{"Mode":"DISTRIBUTED"},"StartAt":"P","States":{"P":{"Type":"Succeed"}}},"End":true}}}`, nil, "ExecutionType: must be STANDARD or EXPRESS"},
		{dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:getObject","ReaderConfig":{"InputType":"PARQUET"},"Parameters":{}}`), nil, "PARQUET is not supported"},
		{dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:getObject","ReaderConfig":{"CSVHeaderLocation":"GIVEN"},"Parameters":{}}`), nil, "CSVHeaders: required"},
		{dmapDef(`"ItemBatcher":{}`), nil, "MaxItemsPerBatch or MaxInputBytesPerBatch is required"},
		{dmapDef(`"ResultWriter":{"Resource":"arn:aws:states:::s3:getObject"}`), nil, "ResultWriter.Resource: must be"},
		{dmapDef(`"Label":"bad label"`), nil, "Label"},
		{`{"StartAt":"M","States":{"M":{"Type":"Map","ItemProcessor":{"ProcessorConfig":{"Mode":"DISTRIBUTED","ExecutionType":"EXPRESS"},
		  "StartAt":"T","States":{"T":{"Type":"Task","Resource":"arn:aws:states:::ecs:runTask.sync","End":true}}},"End":true}}}`, nil, "not supported in Express workflows"},
		{`{"StartAt":"M","States":{"M":{"Type":"Map","ItemReader":{},"ItemProcessor":{"StartAt":"P","States":{"P":{"Type":"Succeed"}}},"End":true}}}`, nil, "only allowed in a distributed Map"},
	}
	for _, c := range cases {
		_, err := sfntest.New([]byte(c.def), c.opts...)
		assert.ErrorContains(t, err, c.want, c.def)
	}
}

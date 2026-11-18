package sfntest_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/winebarrel/sfntest"
)

func jsonataDmapDef(fields string) string {
	return `{"QueryLanguage":"JSONata","StartAt":"M","States":{"M":{"Type":"Map",` + fields + `,` + echoProcessor + `,"End":true}}}`
}

func TestDistributedMapErrors(t *testing.T) {
	files := map[string][]byte{
		"b/ok.json":       []byte(`[1,2]`),
		"b/bad.csv":       []byte(`"open`),
		"b/bad.json":      []byte(`{`),
		"inv/bad.json":    []byte(`{`),
		"inv/m.json":      []byte(`{"fileSchema":"A","files":[{"key":"missing.csv"}]}`),
		"inv/badcsv.json": []byte(`{"fileSchema":"A","files":[{"key":"bad.csv"}]}`),
		"inv/bad.csv":     []byte(`"open`),
		"ath/bad-uri":     []byte("http://x/y\n"),
		"ath/missing":     []byte("s3://b/missing.csv\n"),
	}
	api := &s3Mock{files: files}

	cases := []struct {
		name  string
		def   string
		input string
		api   sfntest.API
		want  string
	}{
		{"inputpath", dmapDef(`"InputPath":"$.x"`), `{}`, nil, "States.Runtime"},
		{"itemspath missing", dmapDef(`"ItemsPath":"$.x"`), `{}`, nil, "States.Runtime"},
		{"itemspath scalar", dmapDef(`"ItemsPath":"$.x"`), `{"x":1}`, nil, "States.Runtime"},
		{"jsonata items eval", jsonataDmapDef(`"Items":"{% $states.input.x %}"`), `{}`, nil, "States.QueryEvaluationError"},
		{"jsonata items scalar", jsonataDmapDef(`"Items":"{% 1 %}"`), `{}`, nil, "States.QueryEvaluationError"},
		{"reader parameters", dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:getObject","Parameters":{"Key.$":"$.x"}}`), `{}`, api, "States.ParameterPathFailure"},
		{"reader arguments scalar", jsonataDmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:getObject","Arguments":"{% 1 %}"}`), `{}`, api, "States.ItemReaderFailed"},
		{"reader csv", dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:getObject","ReaderConfig":{"InputType":"CSV"},"Parameters":{"Bucket":"b","Key":"bad.csv"}}`), `{}`, api, "States.ItemReaderFailed"},
		{"max items path missing", dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:getObject","ReaderConfig":{"MaxItemsPath":"$.x"},"Parameters":{"Bucket":"b","Key":"ok.json"}}`), `{}`, api, "States.Runtime"},
		{"max items path invalid", dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:getObject","ReaderConfig":{"MaxItemsPath":"$.x"},"Parameters":{"Bucket":"b","Key":"ok.json"}}`), `{"x":"a"}`, api, "States.Runtime"},
		{"inventory manifest missing", dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:getObject","ReaderConfig":{"ManifestType":"S3_INVENTORY"},"Parameters":{"Bucket":"inv","Key":"none"}}`), `{}`, api, "States.ItemReaderFailed"},
		{"inventory manifest invalid", dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:getObject","ReaderConfig":{"ManifestType":"S3_INVENTORY"},"Parameters":{"Bucket":"inv","Key":"bad.json"}}`), `{}`, api, "States.ItemReaderFailed"},
		{"inventory data missing", dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:getObject","ReaderConfig":{"ManifestType":"S3_INVENTORY"},"Parameters":{"Bucket":"inv","Key":"m.json"}}`), `{}`, api, "States.ItemReaderFailed"},
		{"inventory data invalid", dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:getObject","ReaderConfig":{"ManifestType":"S3_INVENTORY"},"Parameters":{"Bucket":"inv","Key":"badcsv.json"}}`), `{}`, api, "States.ItemReaderFailed"},
		{"athena manifest missing", dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:getObject","ReaderConfig":{"ManifestType":"ATHENA_DATA"},"Parameters":{"Bucket":"ath","Key":"none"}}`), `{}`, api, "States.ItemReaderFailed"},
		{"athena bad uri", dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:getObject","ReaderConfig":{"ManifestType":"ATHENA_DATA"},"Parameters":{"Bucket":"ath","Key":"bad-uri"}}`), `{}`, api, "States.ItemReaderFailed"},
		{"athena data missing", dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:getObject","ReaderConfig":{"ManifestType":"ATHENA_DATA"},"Parameters":{"Bucket":"ath","Key":"missing"}}`), `{}`, api, "States.ItemReaderFailed"},
		{"flatten object missing", dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:listObjectsV2","ReaderConfig":{"InputType":"JSON","Transformation":"LOAD_AND_FLATTEN"},"Parameters":{"Bucket":"b"}}`), `{}`,
			&s3Mock{files: files, lists: []string{`{"Contents":[{"Key":"none.json"}]}`}}, "States.ItemReaderFailed"},
		{"list invalid json", dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:listObjectsV2","Parameters":{"Bucket":"b"}}`), `{}`, &s3Mock{lists: []string{`{`}}, "States.ItemReaderFailed"},
		{"list not object", dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:listObjectsV2","Parameters":{"Bucket":"b"}}`), `{}`, &s3Mock{lists: []string{`[]`}}, "States.ItemReaderFailed"},
		{"list contents", dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:listObjectsV2","Parameters":{"Bucket":"b"}}`), `{}`, &s3Mock{lists: []string{`{"Contents":[1]}`}}, "States.ItemReaderFailed"},
		{"list item", dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:listObjectsV2","Parameters":{"Bucket":"b"}}`), `{}`, &s3Mock{lists: []string{`{"Contents":[{"LastModified":true}]}`}}, "States.ItemReaderFailed"},
		{"list error", dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:listObjectsV2","Parameters":{"Bucket":"b"}}`), `{}`, failAPI, "States.ItemReaderFailed"},
		{"itemselector", dmapDef(`"ItemSelector":{"a.$":"$.x"}`), `[1]`, nil, "States.ParameterPathFailure"},
		{"batch size path", dmapDef(`"ItemBatcher":{"MaxItemsPerBatchPath":"$.x"}`), `[1]`, nil, "States.Runtime"},
		{"batch bytes path", dmapDef(`"ItemBatcher":{"MaxItemsPerBatch":1,"MaxInputBytesPerBatchPath":"$.x"}`), `[1]`, nil, "States.Runtime"},
		{"batch input", dmapDef(`"ItemBatcher":{"MaxItemsPerBatch":1,"BatchInput":{"a.$":"$.x"}}`), `[1]`, nil, "States.ParameterPathFailure"},
		{"tolerated count path", `{"StartAt":"M","States":{"M":{"Type":"Map","ToleratedFailureCountPath":"$.x",` + failOddProcessor + `,"End":true}}}`, `[1]`, nil, "States.Runtime"},
		{"tolerated percentage path", `{"StartAt":"M","States":{"M":{"Type":"Map","ToleratedFailurePercentagePath":"$.x",` + failOddProcessor + `,"End":true}}}`, `[1]`, nil, "States.Runtime"},
		{"jsonata tolerated negative", `{"QueryLanguage":"JSONata","StartAt":"M","States":{"M":{"Type":"Map","ToleratedFailureCount":"{% -1 %}","ItemProcessor":{"ProcessorConfig":{"Mode":"DISTRIBUTED","ExecutionType":"STANDARD"},"StartAt":"F","States":{"F":{"Type":"Fail","Error":"X"}}},"End":true}}}`, `[1]`, nil, "States.QueryEvaluationError"},
		{"jsonata tolerated eval", `{"QueryLanguage":"JSONata","StartAt":"M","States":{"M":{"Type":"Map","ToleratedFailureCount":"{% $x %}","ItemProcessor":{"ProcessorConfig":{"Mode":"DISTRIBUTED","ExecutionType":"STANDARD"},"StartAt":"F","States":{"F":{"Type":"Fail","Error":"X"}}},"End":true}}}`, `[1]`, nil, "States.QueryEvaluationError"},
		{"writer parameters", dmapDef(`"ResultWriter":{"Resource":"arn:aws:states:::s3:putObject","Parameters":{"Bucket.$":"$.x"}}`), `[1]`, okAPI, "States.ParameterPathFailure"},
		{"writer succeeded", dmapDef(`"ResultWriter":{"Resource":"arn:aws:states:::s3:putObject","Parameters":{"Bucket":"b"}}`), `[1]`, failAPI, "States.ResultWriterFailed"},
		{"writer failed", `{"StartAt":"M","States":{"M":{"Type":"Map","ToleratedFailurePercentage":100,
		  "ResultWriter":{"Resource":"arn:aws:states:::s3:putObject","Parameters":{"Bucket":"b"}},` + failOddProcessor + `,"End":true}}}`, `[1]`, failAPI, "States.ResultWriterFailed"},
		{"resultselector", dmapDef(`"ResultSelector":{"a.$":"$.x"}`), `[1]`, nil, "States.ParameterPathFailure"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := run(t, c.def, c.input, &sfntest.Env{API: c.api})
			assert.Equal(t, sfntest.Failed, res.Status)
			assert.Equal(t, c.want, res.Error, res.Cause)
		})
	}
}

// putFailAPI fails only the manifest write.
type putFailAPI struct{}

func (p *putFailAPI) Call(_ context.Context, _ string, params []byte) ([]byte, error) {
	var m map[string]any
	_ = json.Unmarshal(params, &m)
	if key, _ := m["Key"].(string); len(key) > 13 && key[len(key)-13:] == "manifest.json" {
		return nil, sfntest.Error("S3.Denied", "")
	}
	return []byte(`{}`), nil
}

func TestDistributedMapMoreBehaviors(t *testing.T) {
	t.Run("manifest write failure", func(t *testing.T) {
		res := run(t, dmapDef(`"ResultWriter":{"Resource":"arn:aws:states:::s3:putObject","Parameters":{"Bucket":"b"}}`), `[1]`, &sfntest.Env{API: &putFailAPI{}})
		assert.Equal(t, "States.ResultWriterFailed", res.Error)
	})

	t.Run("json output to S3 without prefix", func(t *testing.T) {
		m := &s3Mock{}
		res := run(t, dmapDef(`"ResultWriter":{"Resource":"arn:aws:states:::s3:putObject","Parameters":{"Bucket":"b"},"WriterConfig":{"Transformation":"FLATTEN"}}`), `[[1,2],3]`, &sfntest.Env{API: m})
		require.Equal(t, sfntest.Succeeded, res.Status, res.Cause)
		found := false
		for k, v := range m.puts {
			if len(k) > 16 && k[len(k)-16:] == "SUCCEEDED_0.json" {
				assert.Equal(t, `[1,2,3]`, v)
				found = true
			}
		}
		assert.True(t, found)
	})

	t.Run("jsonata writer arguments and object items", func(t *testing.T) {
		m := &s3Mock{}
		res := run(t, jsonataDmapDef(`"Items":{"a":1,"b":2},"ItemSelector":{"k":"{% $states.context.Map.Item.Key %}"},
		  "ResultWriter":{"Resource":"arn:aws:states:::s3:putObject","Arguments":{"Bucket":"{% 'out' %}"}}`), `{}`, &sfntest.Env{API: m})
		require.Equal(t, sfntest.Succeeded, res.Status, res.Cause)
		var out map[string]any
		require.NoError(t, json.Unmarshal(res.Output, &out))
		assert.Equal(t, "out", out["ResultWriterDetails"].(map[string]any)["Bucket"])
	})

	t.Run("jsonata fractional integers", func(t *testing.T) {
		res := run(t, jsonataDmapDef(`"ItemBatcher":{"MaxItemsPerBatch":"{% 1.9 %}"}`), `[1,2]`, nil)
		assert.JSONEq(t, `[{"Items":[1]},{"Items":[2]}]`, string(res.Output))
	})

	t.Run("tolerated paths", func(t *testing.T) {
		def := `{"StartAt":"M","States":{"M":{"Type":"Map","ToleratedFailureCountPath":"$.c","ToleratedFailurePercentagePath":"$.p","ItemsPath":"$.items",` + failOddProcessor + `,"End":true}}}`
		res := run(t, def, `{"c":1,"p":50,"items":[1,2]}`, nil)
		assert.Equal(t, sfntest.Succeeded, res.Status, res.Cause)
		res = run(t, def, `{"c":1,"p":10,"items":[1,2]}`, nil)
		assert.Equal(t, "States.ExceedToleratedFailureThreshold", res.Error)
	})

	t.Run("athena manifest", func(t *testing.T) {
		m := &s3Mock{files: map[string][]byte{
			"ath/q-manifest.csv": []byte("s3://data/r1.csv\n\ns3://data/r2.csv\n"),
			"data/r1.csv":        []byte("a,b\n1,2\n"),
			"data/r2.csv":        []byte("a,b\n3,4\n"),
		}}
		res := run(t, dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:getObject","ReaderConfig":{"ManifestType":"ATHENA_DATA","InputType":"CSV"},"Parameters":{"Bucket":"ath","Key":"q-manifest.csv"}}`), `{}`, &sfntest.Env{API: m})
		require.Equal(t, sfntest.Succeeded, res.Status, res.Cause)
		assert.JSONEq(t, `[{"a":"1","b":"2"},{"a":"3","b":"4"}]`, string(res.Output))
	})

	t.Run("aborts", func(t *testing.T) {
		sm, err := sfntest.New([]byte(dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:getObject","Parameters":{"Bucket":"b","Key":"k"}}`)))
		require.NoError(t, err)
		_, err = sm.Run(context.Background(), []byte(`{}`), nil)
		assert.ErrorContains(t, err, "Env.API is not set")
		_, err = sm.Run(context.Background(), []byte(`{}`), &sfntest.Env{API: sfntest.APIFunc(func(context.Context, string, []byte) ([]byte, error) {
			return nil, errors.New("bug")
		})})
		assert.ErrorContains(t, err, "bug")

		// a fatal error in a child execution
		sm, err = sfntest.New([]byte(`{"StartAt":"M","States":{"M":{"Type":"Map","ItemProcessor":{"ProcessorConfig":{"Mode":"DISTRIBUTED","ExecutionType":"STANDARD"},
		  "StartAt":"T","States":{"T":{"Type":"Task","Resource":"r","End":true}}},"End":true}}}`))
		require.NoError(t, err)
		_, err = sm.Run(context.Background(), []byte(`[1]`), nil)
		assert.ErrorContains(t, err, "Env.API is not set")
	})
}

func TestDistributedMapValidationMore(t *testing.T) {
	reader := func(cfg string) string {
		return dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:getObject","ReaderConfig":` + cfg + `,"Parameters":{}}`)
	}
	cases := map[string]string{
		dmapDef(`"ToleratedFailureCount":1,"ToleratedFailureCountPath":"$.x"`):                                           "cannot be used together",
		dmapDef(`"ToleratedFailureCount":-1`):                                                                            "must be a non-negative integer",
		dmapDef(`"ToleratedFailurePercentage":101`):                                                                      "between 0 and 100",
		jsonataDmapDef(`"ToleratedFailureCountPath":"$.x"`):                                                              "not allowed",
		dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:putObject","Parameters":{}}`):                             "ItemReader.Resource: must be",
		jsonataDmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:getObject","Parameters":{}}`):                      `"Parameters" is not allowed`,
		dmapDef(`"ItemReader":{"Resource":"arn:aws:states:::s3:getObject","Arguments":{}}`):                              `"Arguments" is not allowed`,
		reader(`{"InputType":"XML"}`):                                                                                    "unknown input type",
		reader(`{"ManifestType":"S3_INVENTORY","InputType":"CSV"}`):                                                      "must not be specified",
		reader(`{"ManifestType":"X"}`):                                                                                   "unknown manifest type",
		reader(`{"Transformation":"LOAD_AND_FLATTEN"}`):                                                                  "InputType is required",
		reader(`{"Transformation":"X"}`):                                                                                 "must be NONE or LOAD_AND_FLATTEN",
		reader(`{"CSVDelimiter":"X"}`):                                                                                   "unknown delimiter",
		reader(`{"CSVHeaderLocation":"X"}`):                                                                              "FIRST_ROW or GIVEN",
		dmapDef(`"ResultWriter":{}`):                                                                                     "Resource or WriterConfig is required",
		dmapDef(`"ResultWriter":{"WriterConfig":{"OutputType":"X"}}`):                                                    "JSON or JSONL",
		dmapDef(`"ResultWriter":{"WriterConfig":{"Transformation":"X"}}`):                                                "NONE, COMPACT or FLATTEN",
		jsonataDmapDef(`"ResultWriter":{"Resource":"arn:aws:states:::s3:putObject","Arguments":{"Bucket":"{% 1 + %}"}}`): "ResultWriter.Arguments",
		jsonataDmapDef(`"ItemBatcher":{"MaxItemsPerBatch":"{% 1 + %}"}`):                                                 "MaxItemsPerBatch",
		jsonataDmapDef(`"ItemBatcher":{"MaxItemsPerBatch":1,"BatchInput":"{% 1 + %}"}`):                                  "BatchInput",
		jsonataDmapDef(`"Items":"{% 1 + %}"`):                                                                            "Items",
	}
	for def, want := range cases {
		_, err := sfntest.New([]byte(def))
		assert.ErrorContains(t, err, want, def)
	}
}

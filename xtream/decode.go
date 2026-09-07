package xtream

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/goccy/go-json"
)

type apiJSONDecoder interface {
	decodeJSON(*json.Decoder) error
}

type listResponse[T any] []T

type numberedValue[T any] struct {
	n     int
	key   string
	value T
}

func (r *listResponse[T]) decodeJSON(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	if token == nil {
		*r = listResponse[T]{}
		return nil
	}

	delim, ok := token.(json.Delim)
	if !ok {
		return fmt.Errorf("xtream list must be an array or numbered object")
	}

	switch delim {
	case '[':
		values := make(listResponse[T], 0)
		for dec.More() {
			var value T
			if err := dec.Decode(&value); err != nil {
				return err
			}
			values = append(values, value)
		}
		if _, err := dec.Token(); err != nil {
			return err
		}
		*r = values
		return nil
	case '{':
		values := make([]numberedValue[T], 0)
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("xtream list object has a non-string key")
			}
			n, err := strconv.Atoi(key)
			if err != nil || n < 0 {
				return fmt.Errorf("xtream list object has non-numeric key %q", key)
			}
			var value T
			if err := dec.Decode(&value); err != nil {
				return err
			}
			values = append(values, numberedValue[T]{n: n, key: key, value: value})
		}
		if _, err := dec.Token(); err != nil {
			return err
		}
		sort.Slice(values, func(i, j int) bool {
			if values[i].n != values[j].n {
				return values[i].n < values[j].n
			}
			return values[i].key < values[j].key
		})
		result := make(listResponse[T], len(values))
		for i := range values {
			result[i] = values[i].value
		}
		*r = result
		return nil
	default:
		return fmt.Errorf("xtream list must be an array or numbered object")
	}
}

func decodeAPIResponse[T any](reader io.Reader, result *T) error {
	dec := json.NewDecoder(reader)
	var err error
	if custom, ok := any(result).(apiJSONDecoder); ok {
		err = custom.decodeJSON(dec)
	} else {
		err = dec.Decode(result)
	}
	if err != nil {
		return err
	}

	var trailing json.RawMessage
	err = dec.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("xtream response contains multiple JSON values")
}

func retryableJSONError(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var syntaxErr *json.SyntaxError
	return errors.As(err, &syntaxErr)
}

type rawNumber string

func (n *rawNumber) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return io.ErrUnexpectedEOF
	}
	if bytes.Equal(data, []byte("null")) {
		*n = ""
		return nil
	}

	value := string(data)
	if data[0] == '"' {
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
	}
	value = strings.TrimSpace(value)
	if value == "" {
		*n = ""
		return nil
	}
	if _, err := strconv.ParseUint(value, 10, 64); err != nil {
		*n = ""
		return nil
	}
	*n = rawNumber(value)
	return nil
}

func (n rawNumber) String() string {
	return string(n)
}

func (n rawNumber) Int() int {
	value, _ := strconv.Atoi(string(n))
	return value
}

func (c *RawCategory) UnmarshalJSON(data []byte) error {
	var value struct {
		CategoryID   rawNumber `json:"category_id"`
		CategoryName string    `json:"category_name"`
		ParentID     rawNumber `json:"parent_id"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	c.CategoryID = json.Number(value.CategoryID.String())
	c.CategoryName = value.CategoryName
	c.ParentID = json.Number(value.ParentID.String())
	return nil
}

func usableEntry(name string, id rawNumber) bool {
	if strings.TrimSpace(name) == "" {
		return false
	}
	value, err := strconv.ParseUint(id.String(), 10, 64)
	return err == nil && value > 0
}

type rawEpisodeInfo struct {
	MovieImage string `json:"movie_image"`
}

func (i *rawEpisodeInfo) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return io.ErrUnexpectedEOF
	}
	if data[0] != '{' {
		*i = rawEpisodeInfo{}
		return nil
	}
	type plain rawEpisodeInfo
	return json.Unmarshal(data, (*plain)(i))
}

func (e RawEpisode) Image() string {
	if e.MovieImage != "" {
		return e.MovieImage
	}
	return e.Info.MovieImage
}

type episodeGroups map[string][]RawEpisode

func (g *episodeGroups) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return io.ErrUnexpectedEOF
	}
	if bytes.Equal(data, []byte("null")) {
		*g = episodeGroups{}
		return nil
	}
	if data[0] == '[' {
		var values []json.RawMessage
		if err := json.Unmarshal(data, &values); err != nil {
			return err
		}
		if len(values) != 0 {
			return fmt.Errorf("xtream episodes array must be empty or grouped by season")
		}
		*g = episodeGroups{}
		return nil
	}
	if data[0] != '{' {
		return fmt.Errorf("xtream episodes must be an object")
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	groups := make(episodeGroups, len(raw))
	for season, payload := range raw {
		var episodes listResponse[RawEpisode]
		if err := decodeAPIResponse(bytes.NewReader(payload), &episodes); err != nil {
			return fmt.Errorf("season %q: %w", season, err)
		}
		groups[season] = []RawEpisode(episodes)
	}
	*g = groups
	return nil
}

func (i *RawSeriesInfo) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return io.ErrUnexpectedEOF
	}
	if bytes.Equal(data, []byte("null")) {
		i.Episodes = episodeGroups{}
		return nil
	}
	if data[0] == '[' {
		var values []json.RawMessage
		if err := json.Unmarshal(data, &values); err != nil {
			return err
		}
		if len(values) != 0 {
			return fmt.Errorf("xtream series info must be an object or empty array")
		}
		i.Episodes = episodeGroups{}
		return nil
	}
	if data[0] != '{' {
		return fmt.Errorf("xtream series info must be an object or empty array")
	}
	type plain RawSeriesInfo
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if decoded.Episodes == nil {
		decoded.Episodes = episodeGroups{}
	}
	*i = RawSeriesInfo(decoded)
	return nil
}

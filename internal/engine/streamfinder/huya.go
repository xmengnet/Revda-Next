package streamfinder

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type HuyaFinder struct {
	client *http.Client
}

func NewHuyaFinder() *HuyaFinder {
	return &HuyaFinder{
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func genNNumber(length int) string {
	if length <= 0 {
		return ""
	}
	res := make([]byte, length)
	res[0] = byte('1' + rand.IntN(9))
	for i := 1; i < length; i++ {
		res[i] = byte('0' + rand.IntN(10))
	}
	return string(res)
}

func genHuyaParams(antiCode string, streamName string) string {
	q, _ := url.ParseQuery(antiCode)

	uid := genNNumber(13)
	q.Set("t", "102")
	q.Set("ctype", "tars_mp")

	wsTime := fmt.Sprintf("%x", time.Now().Unix()+21600)

	uidInt, _ := strconv.ParseInt(uid, 10, 64)
	seqID := fmt.Sprintf("%d", time.Now().UnixMilli()+uidInt)

	fmBytes, err := base64.StdEncoding.DecodeString(q.Get("fm"))
	if err != nil {
		return ""
	}
	wsSecretPrefix := strings.Split(string(fmBytes), "_")[0]

	h1 := md5.Sum([]byte(fmt.Sprintf("%s|%s|%s", seqID, q.Get("ctype"), q.Get("t"))))
	wsSecretHash := hex.EncodeToString(h1[:])

	h2 := md5.Sum([]byte(fmt.Sprintf("%s_%s_%s_%s_%s", wsSecretPrefix, uid, streamName, wsSecretHash, wsTime)))
	wsSecret := hex.EncodeToString(h2[:])

	values := url.Values{}
	values.Set("wsSecret", wsSecret)
	values.Set("wsTime", wsTime)
	values.Set("seqid", seqID)
	values.Set("ctype", q.Get("ctype"))
	values.Set("ver", "1")
	values.Set("fs", q.Get("fs"))
	values.Set("uid", uid)
	values.Set("uuid", genNNumber(10))
	values.Set("t", q.Get("t"))
	values.Set("sv", "2401231033")

	for _, extra := range []string{"sphdcdn", "sphdDC", "sphd", "exsphd"} {
		if val := q.Get(extra); val != "" {
			values.Set(extra, val)
		}
	}

	return values.Encode()
}

func (h *HuyaFinder) Resolve(ctx context.Context, roomURL string) (*StreamInfo, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", roomURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	req.Header.Set("Referer", "https://www.huya.com/")

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	bodyStr := string(body)

	reConfig := regexp.MustCompile(`(?s)hyPlayerConfig.*?stream:(.*?)\s*};`)
	reRoomData := regexp.MustCompile(`var\s+TT_ROOM_DATA\s+=\s+({.+?});`)

	mConfig := reConfig.FindStringSubmatch(bodyStr)
	mRoomData := reRoomData.FindStringSubmatch(bodyStr)

	if len(mConfig) < 2 || len(mRoomData) < 2 {
		return nil, errors.New("huya player config not found")
	}

	var roomData struct {
		IsOn         bool   `json:"isOn"`
		Introduction string `json:"introduction"`
	}
	_ = json.Unmarshal([]byte(mRoomData[1]), &roomData)
	if !roomData.IsOn {
		return nil, Offline("huya streamer is not on")
	}

	var streamConfig struct {
		Data []struct {
			GameStreamInfoList []struct {
				SFlvURL       string `json:"sFlvUrl"`
				SFlvURLSuffix string `json:"sFlvUrlSuffix"`
				SStreamName   string `json:"sStreamName"`
				SFlvAntiCode  string `json:"sFlvAntiCode"`
			} `json:"gameStreamInfoList"`
		} `json:"data"`
	}

	if err := json.Unmarshal([]byte(mConfig[1]), &streamConfig); err != nil {
		return nil, fmt.Errorf("huya parse stream json error: %w", err)
	}

	if len(streamConfig.Data) == 0 || len(streamConfig.Data[0].GameStreamInfoList) == 0 {
		return nil, errors.New("huya game stream list empty")
	}

	item := streamConfig.Data[0].GameStreamInfoList[0]
	params := genHuyaParams(item.SFlvAntiCode, item.SStreamName)
	rawStreamURL := fmt.Sprintf("%s/%s.%s?%s", item.SFlvURL, item.SStreamName, item.SFlvURLSuffix, params)
	finalURL := html.UnescapeString(rawStreamURL)

	return &StreamInfo{
		Title: roomData.Introduction,
		URL:   finalURL,
		Headers: map[string]string{
			"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36",
			"Referer":    "https://www.huya.com/",
		},
	}, nil
}

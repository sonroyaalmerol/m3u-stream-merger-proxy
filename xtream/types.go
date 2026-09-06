package xtream

import "encoding/json"

type RawCategory struct {
	CategoryID   json.Number `json:"category_id"`
	CategoryName string      `json:"category_name"`
	ParentID     json.Number `json:"parent_id"`
}

type RawLiveStream struct {
	Num          int         `json:"num"`
	Name         string      `json:"name"`
	StreamType   string      `json:"stream_type"`
	StreamID     json.Number `json:"stream_id"`
	StreamIcon   string      `json:"stream_icon"`
	EPGChannelID string      `json:"epg_channel_id"`
	Added        string      `json:"added"`
	CategoryID   json.Number `json:"category_id"`
	CustomSID    string      `json:"custom_sid"`
	DirectSource string      `json:"direct_source"`
}

type RawVodStream struct {
	Num                int         `json:"num"`
	Name               string      `json:"name"`
	StreamType         string      `json:"stream_type"`
	StreamID           json.Number `json:"stream_id"`
	StreamIcon         string      `json:"stream_icon"`
	Rating             string      `json:"rating"`
	Added              string      `json:"added"`
	CategoryID         json.Number `json:"category_id"`
	ContainerExtension string      `json:"container_extension"`
	CustomSID          string      `json:"custom_sid"`
	DirectSource       string      `json:"direct_source"`
}

type RawSeries struct {
	Num          int         `json:"num"`
	Name         string      `json:"name"`
	SeriesID     json.Number `json:"series_id"`
	Cover        string      `json:"cover"`
	Plot         string      `json:"plot"`
	Cast         string      `json:"cast"`
	Director     string      `json:"director"`
	Genre        string      `json:"genre"`
	ReleaseDate  string      `json:"releaseDate"`
	LastModified string      `json:"last_modified"`
	Rating       string      `json:"rating"`
	CategoryID   json.Number `json:"category_id"`
}

type RawEpisode struct {
	ID                 json.Number `json:"id"`
	EpisodeNum         int         `json:"episode_num"`
	Title              string      `json:"title"`
	ContainerExtension string      `json:"container_extension"`
	Season             int         `json:"season"`
	MovieImage         string      `json:"movie_image"`
	Plot               string      `json:"plot"`
	Duration           string      `json:"duration"`
}

type RawSeriesInfo struct {
	Info struct {
		Name         string `json:"name"`
		Cover        string `json:"cover"`
		Plot         string `json:"plot"`
		Cast         string `json:"cast"`
		Director     string `json:"director"`
		Genre        string `json:"genre"`
		ReleaseDate  string `json:"releaseDate"`
		Rating       string `json:"rating"`
		LastModified int64  `json:"last_modified"`
	} `json:"info"`
	Episodes map[string][]RawEpisode `json:"episodes"`
}

type UserInfo struct {
	Username             string   `json:"username"`
	Password             string   `json:"password"`
	Message              string   `json:"message"`
	Auth                 int      `json:"auth"`
	Status               string   `json:"status"`
	ExpDate              string   `json:"exp_date"`
	IsTrial              string   `json:"is_trial"`
	ActiveCons           string   `json:"active_cons"`
	CreatedAt            int64    `json:"created_at"`
	MaxConnections       string   `json:"max_connections"`
	AllowedOutputFormats []string `json:"allowed_output_formats"`
}

type ServerInfo struct {
	URL            string `json:"url"`
	Port           string `json:"port"`
	HTTPSPort      string `json:"https_port"`
	ServerProtocol string `json:"server_protocol"`
	RTMPPort       string `json:"rtmp_port"`
	Timezone       string `json:"timezone"`
	TimestampNow   int64  `json:"timestamp_now"`
	TimeNow        string `json:"time_now"`
}

type RootResponse struct {
	UserInfo   UserInfo   `json:"user_info"`
	ServerInfo ServerInfo `json:"server_info"`
}

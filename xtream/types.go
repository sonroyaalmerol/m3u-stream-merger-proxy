package xtream

import "encoding/json"

const (
	TypeLive   = "live"
	TypeMovie  = "movie"
	TypeSeries = "series"
)

type RawCategory struct {
	CategoryID   json.Number `json:"category_id"`
	CategoryName string      `json:"category_name"`
	ParentID     json.Number `json:"parent_id"`
}

type RawLiveStream struct {
	Name         string    `json:"name"`
	StreamID     rawNumber `json:"stream_id"`
	StreamIcon   string    `json:"stream_icon"`
	EPGChannelID string    `json:"epg_channel_id"`
	CategoryID   rawNumber `json:"category_id"`
}

type RawVodStream struct {
	Name               string    `json:"name"`
	StreamID           rawNumber `json:"stream_id"`
	StreamIcon         string    `json:"stream_icon"`
	CategoryID         rawNumber `json:"category_id"`
	ContainerExtension string    `json:"container_extension"`
}

type RawSeries struct {
	Name       string    `json:"name"`
	SeriesID   rawNumber `json:"series_id"`
	Cover      string    `json:"cover"`
	CategoryID rawNumber `json:"category_id"`
}

type RawEpisode struct {
	ID                 rawNumber      `json:"id"`
	EpisodeNum         rawNumber      `json:"episode_num"`
	ContainerExtension string         `json:"container_extension"`
	MovieImage         string         `json:"movie_image"`
	Info               rawEpisodeInfo `json:"info"`
}

type RawSeriesInfo struct {
	Episodes episodeGroups `json:"episodes"`
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

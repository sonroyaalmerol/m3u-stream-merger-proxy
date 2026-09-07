package xtream

import "encoding/json"

type LiveStreamOut struct {
	Num               int           `json:"num"`
	Name              string        `json:"name"`
	StreamType        string        `json:"stream_type"`
	StreamID          json.Number   `json:"stream_id"`
	StreamIcon        string        `json:"stream_icon"`
	EPGChannelID      string        `json:"epg_channel_id"`
	Added             string        `json:"added"`
	IsAdult           string        `json:"is_adult"`
	CategoryID        json.Number   `json:"category_id"`
	CategoryIDs       []json.Number `json:"category_ids"`
	CustomSID         string        `json:"custom_sid"`
	TVArchive         int           `json:"tv_archive"`
	DirectSource      string        `json:"direct_source"`
	TVArchiveDuration int           `json:"tv_archive_duration"`
}

type VodStreamOut struct {
	Num                int           `json:"num"`
	Name               string        `json:"name"`
	StreamType         string        `json:"stream_type"`
	StreamID           json.Number   `json:"stream_id"`
	StreamIcon         string        `json:"stream_icon"`
	Rating             string        `json:"rating"`
	Rating5Based       float64       `json:"rating_5based"`
	Added              string        `json:"added"`
	IsAdult            string        `json:"is_adult"`
	CategoryID         json.Number   `json:"category_id"`
	CategoryIDs        []json.Number `json:"category_ids"`
	ContainerExtension string        `json:"container_extension"`
	CustomSID          string        `json:"custom_sid"`
	DirectSource       string        `json:"direct_source"`
}

type SeriesOut struct {
	Num            int           `json:"num"`
	Name           string        `json:"name"`
	SeriesID       json.Number   `json:"series_id"`
	Cover          string        `json:"cover"`
	Plot           string        `json:"plot"`
	Cast           string        `json:"cast"`
	Director       string        `json:"director"`
	Genre          string        `json:"genre"`
	ReleaseDate    string        `json:"releaseDate"`
	LastModified   string        `json:"last_modified"`
	Rating         string        `json:"rating"`
	Rating5Based   float64       `json:"rating_5based"`
	BackdropPath   []string      `json:"backdrop_path"`
	YoutubeTrailer string        `json:"youtube_trailer"`
	EpisodeRunTime string        `json:"episode_run_time"`
	CategoryID     json.Number   `json:"category_id"`
	CategoryIDs    []json.Number `json:"category_ids"`
}

type SeasonOut struct {
	AirDate      string      `json:"air_date"`
	EpisodeCount int         `json:"episode_count"`
	ID           json.Number `json:"id"`
	Name         string      `json:"name"`
	Overview     string      `json:"overview"`
	SeasonNumber int         `json:"season_number"`
	Cover        string      `json:"cover"`
	CoverBig     string      `json:"cover_big"`
}

type EpisodeInfoOut struct {
	MovieImage   string  `json:"movie_image"`
	Plot         string  `json:"plot"`
	Duration     string  `json:"duration"`
	DurationSecs int     `json:"duration_secs"`
	Bitrate      int     `json:"bitrate"`
	Rating       float64 `json:"rating"`
	Season       int     `json:"season"`
}

type EpisodeOut struct {
	ID                 json.Number    `json:"id"`
	EpisodeNum         int            `json:"episode_num"`
	Title              string         `json:"title"`
	ContainerExtension string         `json:"container_extension"`
	Info               EpisodeInfoOut `json:"info"`
	CustomSID          string         `json:"custom_sid"`
	Added              string         `json:"added"`
	Season             int            `json:"season"`
	DirectSource       string         `json:"direct_source"`
}

type SeriesInfoDetailOut struct {
	Name           string   `json:"name"`
	Cover          string   `json:"cover"`
	Plot           string   `json:"plot"`
	Cast           string   `json:"cast"`
	Director       string   `json:"director"`
	Genre          string   `json:"genre"`
	ReleaseDate    string   `json:"releaseDate"`
	LastModified   string   `json:"last_modified"`
	Rating         string   `json:"rating"`
	Rating5Based   float64  `json:"rating_5based"`
	BackdropPath   []string `json:"backdrop_path"`
	YoutubeTrailer string   `json:"youtube_trailer"`
	EpisodeRunTime string   `json:"episode_run_time"`
	CategoryID     string   `json:"category_id"`
}

type SeriesInfoOut struct {
	Seasons  []SeasonOut             `json:"seasons"`
	Info     SeriesInfoDetailOut     `json:"info"`
	Episodes map[string][]EpisodeOut `json:"episodes"`
}

type VodInfoDetailOut struct {
	MovieImage     string   `json:"movie_image"`
	CoverBig       string   `json:"cover_big"`
	TmdbID         string   `json:"tmdb_id"`
	Name           string   `json:"name"`
	ONname         string   `json:"o_name"`
	Genre          string   `json:"genre"`
	Plot           string   `json:"plot"`
	Description    string   `json:"description"`
	Cast           string   `json:"cast"`
	Actors         string   `json:"actors"`
	Director       string   `json:"director"`
	ReleaseDate    string   `json:"releasedate"`
	Rating         string   `json:"rating"`
	Country        string   `json:"country"`
	BackdropPath   []string `json:"backdrop_path"`
	YoutubeTrailer string   `json:"youtube_trailer"`
	Duration       string   `json:"duration"`
	DurationSecs   int      `json:"duration_secs"`
	Bitrate        int      `json:"bitrate"`
}

type VodMovieDataOut struct {
	StreamID           json.Number   `json:"stream_id"`
	Name               string        `json:"name"`
	Added              string        `json:"added"`
	CategoryID         json.Number   `json:"category_id"`
	CategoryIDs        []json.Number `json:"category_ids"`
	ContainerExtension string        `json:"container_extension"`
	CustomSID          string        `json:"custom_sid"`
	DirectSource       string        `json:"direct_source"`
}

type VodInfoOut struct {
	Info      VodInfoDetailOut `json:"info"`
	MovieData VodMovieDataOut  `json:"movie_data"`
}

type EPGListingOut struct {
	ID             json.Number `json:"id"`
	EPGID          json.Number `json:"epg_id"`
	Title          string      `json:"title"`
	Lang           string      `json:"lang"`
	Start          string      `json:"start"`
	End            string      `json:"end"`
	Description    string      `json:"description"`
	ChannelID      string      `json:"channel_id"`
	StartTimestamp int64       `json:"start_timestamp"`
	StopTimestamp  int64       `json:"stop_timestamp"`
	NowPlaying     int         `json:"now_playing"`
	HasArchive     int         `json:"has_archive"`
}

// PanelResponse is the legacy panel_api.php shape: auth plus the whole catalog.
type PanelResponse struct {
	UserInfo          UserInfo                   `json:"user_info"`
	ServerInfo        ServerInfo                 `json:"server_info"`
	Categories        map[string][]RawCategory `json:"categories"`
	AvailableChannels map[string]any           `json:"available_channels"`
}

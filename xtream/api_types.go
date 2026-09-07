package xtream

import "encoding/json"

type LiveStreamOut struct {
	Num               int           `json:"num"`
	Name              string        `json:"name"`
	StreamType        string        `json:"stream_type"`
	StreamID          json.Number   `json:"stream_id"`
	StreamIcon        string        `json:"stream_icon"`
	Thumbnail         string        `json:"thumbnail"`
	EPGChannelID      string        `json:"epg_channel_id"`
	Added             string        `json:"added"`
	IsAdult           string        `json:"is_adult"`
	CategoryID        string        `json:"category_id"`
	CategoryIDs       []json.Number `json:"category_ids"`
	CustomSID         string        `json:"custom_sid"`
	TVArchive         int           `json:"tv_archive"`
	DirectSource      string        `json:"direct_source"`
	TVArchiveDuration int           `json:"tv_archive_duration"`
}

type VodStreamOut struct {
	Num                int           `json:"num"`
	Name               string        `json:"name"`
	Title              string        `json:"title"`
	Year               string        `json:"year"`
	StreamType         string        `json:"stream_type"`
	StreamID           json.Number   `json:"stream_id"`
	StreamIcon         string        `json:"stream_icon"`
	Rating             float64       `json:"rating"`
	Rating5Based       float64       `json:"rating_5based"`
	Genre              string        `json:"genre"`
	Plot               string        `json:"plot"`
	Cast               string        `json:"cast"`
	Director           string        `json:"director"`
	ReleaseDate        string        `json:"release_date"`
	YoutubeTrailer     string        `json:"youtube_trailer"`
	EpisodeRunTime     int           `json:"episode_run_time"`
	Added              string        `json:"added"`
	IsAdult            string        `json:"is_adult"`
	CategoryID         string        `json:"category_id"`
	CategoryIDs        []json.Number `json:"category_ids"`
	ContainerExtension string        `json:"container_extension"`
	CustomSID          string        `json:"custom_sid"`
	DirectSource       string        `json:"direct_source"`
}

type SeriesOut struct {
	Num            int           `json:"num"`
	Name           string        `json:"name"`
	Title          string        `json:"title"`
	Year           string        `json:"year"`
	StreamType     string        `json:"stream_type"`
	SeriesID       json.Number   `json:"series_id"`
	Cover          string        `json:"cover"`
	Plot           string        `json:"plot"`
	Cast           string        `json:"cast"`
	Director       string        `json:"director"`
	Genre          string        `json:"genre"`
	ReleaseDate    string        `json:"release_date"`
	ReleaseDateAlt string        `json:"releaseDate"`
	LastModified   string        `json:"last_modified"`
	Rating         string        `json:"rating"`
	Rating5Based   float64       `json:"rating_5based"`
	BackdropPath   []string      `json:"backdrop_path"`
	YoutubeTrailer string        `json:"youtube_trailer"`
	EpisodeRunTime string        `json:"episode_run_time"`
	CategoryID     string        `json:"category_id"`
	CategoryIDs    []json.Number `json:"category_ids"`
}

type SeasonOut struct {
	AirDate      string      `json:"air_date"`
	EpisodeCount int         `json:"episode_count"`
	ID           json.Number `json:"id"`
	Name         string      `json:"name"`
	Overview     string      `json:"overview"`
	SeasonNumber int         `json:"season_number"`
	VoteAverage  float64     `json:"vote_average"`
	Cover        string      `json:"cover"`
	CoverBig     string      `json:"cover_big"`
}

type EpisodeInfoOut struct {
	TmdbID       int     `json:"tmdb_id"`
	ReleaseDate  string  `json:"release_date"`
	MovieImage   string  `json:"movie_image"`
	CoverBig     string  `json:"cover_big"`
	Plot         string  `json:"plot"`
	Duration     string  `json:"duration"`
	DurationSecs int     `json:"duration_secs"`
	Bitrate      int     `json:"bitrate"`
	Rating       float64 `json:"rating"`
	Season       int     `json:"season"`
}

type EpisodeOut struct {
	ID                 string         `json:"id"`
	EpisodeNum         string         `json:"episode_num"`
	Title              string         `json:"title"`
	ContainerExtension string         `json:"container_extension"`
	Info               EpisodeInfoOut `json:"info"`
	Subtitles          []string       `json:"subtitles"`
	CustomSID          string         `json:"custom_sid"`
	Added              string         `json:"added"`
	Season             int            `json:"season"`
	DirectSource       string         `json:"direct_source"`
}

type SeriesInfoDetailOut struct {
	Name           string        `json:"name"`
	Title          string        `json:"title"`
	Year           string        `json:"year"`
	SeriesID       json.Number   `json:"series_id"`
	Cover          string        `json:"cover"`
	Plot           string        `json:"plot"`
	Cast           string        `json:"cast"`
	Director       string        `json:"director"`
	Genre          string        `json:"genre"`
	ReleaseDate    string        `json:"release_date"`
	ReleaseDateAlt string        `json:"releaseDate"`
	LastModified   string        `json:"last_modified"`
	Rating         string        `json:"rating"`
	Rating5Based   float64       `json:"rating_5based"`
	BackdropPath   []string      `json:"backdrop_path"`
	YoutubeTrailer string        `json:"youtube_trailer"`
	EpisodeRunTime string        `json:"episode_run_time"`
	CategoryID     string        `json:"category_id"`
	CategoryIDs    []json.Number `json:"category_ids"`
}

type SeriesInfoOut struct {
	Seasons  []SeasonOut             `json:"seasons"`
	Info     SeriesInfoDetailOut     `json:"info"`
	Episodes map[string][]EpisodeOut `json:"episodes"`
}

type VodInfoDetailOut struct {
	KinopoiskURL         string   `json:"kinopoisk_url"`
	TmdbID               string   `json:"tmdb_id"`
	Name                 string   `json:"name"`
	ONname               string   `json:"o_name"`
	CoverBig             string   `json:"cover_big"`
	MovieImage           string   `json:"movie_image"`
	ReleaseDate          string   `json:"release_date"`
	ReleaseDateAlt       string   `json:"releasedate"`
	EpisodeRunTime       int      `json:"episode_run_time"`
	YoutubeTrailer       string   `json:"youtube_trailer"`
	Director             string   `json:"director"`
	Actors               string   `json:"actors"`
	Cast                 string   `json:"cast"`
	Description          string   `json:"description"`
	Plot                 string   `json:"plot"`
	Age                  string   `json:"age"`
	MpaaRating           string   `json:"mpaa_rating"`
	RatingCountKinopoisk int      `json:"rating_count_kinopoisk"`
	Country              string   `json:"country"`
	Genre                string   `json:"genre"`
	BackdropPath         []string `json:"backdrop_path"`
	DurationSecs         int      `json:"duration_secs"`
	Duration             string   `json:"duration"`
	Bitrate              int      `json:"bitrate"`
	Rating               float64  `json:"rating"`
	Subtitles            []string `json:"subtitles"`
}

type VodMovieDataOut struct {
	StreamID           json.Number   `json:"stream_id"`
	Name               string        `json:"name"`
	Title              string        `json:"title"`
	Year               string        `json:"year"`
	Added              string        `json:"added"`
	CategoryID         string        `json:"category_id"`
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
	ID             string `json:"id"`
	EPGID          string `json:"epg_id"`
	Title          string `json:"title"`
	Lang           string `json:"lang"`
	Start          string `json:"start"`
	End            string `json:"end"`
	Description    string `json:"description"`
	ChannelID      string `json:"channel_id"`
	StartTimestamp string `json:"start_timestamp"`
	StopTimestamp  string `json:"stop_timestamp"`
	Stop           string `json:"stop"`
	NowPlaying     int    `json:"now_playing"`
	HasArchive     int    `json:"has_archive"`
}

// PanelResponse is the legacy panel_api.php shape: auth plus the whole catalog.
type PanelResponse struct {
	UserInfo          UserInfo                 `json:"user_info"`
	ServerInfo        ServerInfo               `json:"server_info"`
	Categories        map[string][]RawCategory `json:"categories"`
	AvailableChannels map[string]any           `json:"available_channels"`
}

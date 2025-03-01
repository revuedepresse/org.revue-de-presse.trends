package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	_ "github.com/lib/pq"
	"github.com/remeh/sizedwaitgroup"
	_ "github.com/remeh/sizedwaitgroup"
	_ "github.com/ti/nasync"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	_ "gopkg.in/DataDog/dd-trace-go.v1/contrib/database/sql"
	"gopkg.in/zabawaba99/firego.v1"
	"io/ioutil"
	"log"
	_ "math"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

var dryMode bool
var username string
var readFromLocalDb bool
var listAggregateNames bool
var sinceDate string
var sinceAWeekAgo bool
var parallel bool
var quiet bool
var publishersListId string
var aggregateTweetPage int
var aggregateTweetLimit int
var migrateDistinctSourcesOnly bool

const (
	deprecatedPublisherId = "35ca09fb-818c-4456-9d64-7f1401331303"
	tweetPerPage 		  = 100000
)

type Configuration struct {
	List_Id                  string
	Firebase_url             string
	Read_user                string
	Read_password            string
	Read_database            string
	Read_protocol_host_port  string
	Write_user               string
	Write_password           string
	Write_database           string
	Write_protocol_host_port string
	Env                      string
	Service                  string
	ServiceVersion           string
	AgentHost                string
	AgentPort                string
}

type Status struct {
	Id             string `json:"id_str"`
	Text           string `json:"full_text"`
	Retweet_count  int    `json:retweet_count`
	Favorite_count int    `json:favorite_count`
}

type Tweet struct {
	id           int
	publishedAt  string
	username     string
	json         string
	url          string
	tweet        string
	retweets     int
	favorites    int
	twitterId    string
	isRetweet    bool
	canBeRetweet bool
	checkedAt    string
}

// See when init function is run at https://stackoverflow.com/q/24790175/282073
func init() {
	const (
		defaultUsername           = "canardenchaine"
		usage                     = "The username, whose tweets are about to be collected and counted"
		localDbDescription        = "The database from which tweets should be read"
		sinceTodayDescription     = "Store tweets collected over the current day"
		sinceLastWeekDescription  = "Store tweets collected over the last week"
		dryModeDescription  	  = "Show a preview of the SQL queries to be executed without executing them"
	)

	flag.BoolVar(&dryMode, "dry-mode", false, dryModeDescription)
	flag.StringVar(&username, "username", defaultUsername, usage)
	flag.BoolVar(&readFromLocalDb, "read-from-local-db", false, localDbDescription)
	flag.BoolVar(&sinceAWeekAgo, "since-last-week", false, sinceLastWeekDescription)
	flag.StringVar(&sinceDate, "since-date", formatTodayDate(), sinceTodayDescription)
}

func init() {
	const (
		defaultAggregateListing = false
		usage                   = "List aggregate names"
	)

	flag.BoolVar(&listAggregateNames, "aggregate", defaultAggregateListing, usage)
}

func init() {
	const (
		usage           						    = "The id of an publishers list, which tweets are to be printed out"
		defaultLimit    						    = 10
		limitDescription      						= "Maximum tweets be collected"
		defaultPage     						    = 0
		pageDescription       						= "Page from where tweets are collected from"
		defaultQuiet    						    = true
		quietDescription      						= "Quiet mode"
		defaultParallel 						    = true
		parallelDescription   						= "Run in parallel"
		defaultDistinctSourcesOnlyMigrationFlag     = false
		distinctSourcesOnlyMigrationFlagDescription = "Migrate publications from distinct sources only"
	)

	flag.StringVar(&publishersListId, "publishers-list-id", "89f6db28-4d4e-49dc-a2c6-b6bb0e7b12af", usage)
	flag.IntVar(&aggregateTweetLimit, "limit", defaultLimit, limitDescription)
	flag.IntVar(&aggregateTweetPage, "page", defaultPage, pageDescription)
	flag.BoolVar(&quiet, "quiet", defaultQuiet, quietDescription)
	flag.BoolVar(&parallel, "in-parallel", defaultParallel, parallelDescription)
	flag.BoolVar(&migrateDistinctSourcesOnly, "migrate-distinct-sources-only", defaultDistinctSourcesOnlyMigrationFlag, distinctSourcesOnlyMigrationFlagDescription)
}

func main() {
	flag.Parse()

	err, configuration := parseConfiguration()
	handleError(err)

	db := connectToDatabase(configuration)

	// "defer" keyword is described at https://tour.golang.org/flowcontrol/12
	defer db.Close()

	firebase := connectToFirebase(configuration)

	// Won't store publications from distinct sources
	distinctSources := false
	// Won't include retweets
	includeRetweets := false

	if !migrateDistinctSourcesOnly {

		queryTweets(db,
			firebase,
			publishersListId,
			includeRetweets,
			distinctSources,
			aggregateTweetPage,
			aggregateTweetLimit,
			`DESC`,
		)

	}

    // Will store publications from distinct sources
    distinctSources = true

    // Scoping with conditional branch to prevent possible refactoring mistakes in the future
    if distinctSources {

		if !migrateDistinctSourcesOnly {

			// Will include retweets
			includeRetweets := true
			queryTweets(db,
				firebase,
				publishersListId,
				includeRetweets,
				distinctSources,
				aggregateTweetPage,
				aggregateTweetLimit,
				`DESC`,
			)

		}

		// Won't include retweets
		includeRetweets = false
		queryTweets(db,
			firebase,
			publishersListId,
			includeRetweets,
			distinctSources,
			aggregateTweetPage,
			aggregateTweetLimit,
			`DESC`,
		)
	}
}

func removeStatuses(firebase *firego.Firebase) {
	ref, err := firebase.Ref("highlights")
	handleError(err)

	err = ref.Remove()
	handleError(err)
}

func formatTodayDate() string {
	today := time.Now()

	return today.Format("2006-01-02")
}

func connectToDatabase(configuration Configuration) *sql.DB {
	dsn := "postgres://" + configuration.Read_user + string(`:`) + configuration.Read_password +
		`@` + configuration.Read_protocol_host_port + `/` +
		configuration.Read_database + `?sslmode=disable`
	db, err := sql.Open("postgres", dsn)
	handleError(err)

	return db
}

func connectToFirebase(configuration Configuration) *firego.Firebase {
	dir, err := filepath.Abs(filepath.Dir(os.Args[0]))
	handleError(err)

	file, err := ioutil.ReadFile(dir + `/../config.firebase.json`)
	handleError(err)

	conf, err := google.JWTConfigFromJSON(file, "https://www.googleapis.com/auth/userinfo.email",
		"https://www.googleapis.com/auth/firebase.database")
	handleError(err)

	firebase := firego.New(configuration.Firebase_url, conf.Client(oauth2.NoContext))

	return firebase
}

func parseConfiguration() (error, Configuration) {
	dir, err := filepath.Abs(filepath.Dir(os.Args[0]))
	handleError(err)

	file, err := os.Open(dir + `/../config.json`)
	handleError(err)

	decoder := json.NewDecoder(file)
	configuration := Configuration{}
	err = decoder.Decode(&configuration)
	handleError(err)

	return err, configuration
}

func queryTweets(
	db *sql.DB,
	firebase *firego.Firebase,
	publishersListId string,
	includeRetweets bool,
	distinctSources bool,
	page int,
	limit int,
	sortingOrder string) {

	var totalHighlights int

	if migrateDistinctSourcesOnly {
		fmt.Printf("Skipping curated highlights count\n")
	} else {
		totalHighlights = countHighlights(db, distinctSources, limit)
	}

	constraintOnRetweetStatus := ""
	if !includeRetweets {
		constraintOnRetweetStatus = "AND h.is_retweet = false"
	}

	selectClause := `
		SELECT
		CONCAT('https://twitter.com/', ust_full_name, '/status/', ust_status_id) as url,
		s.ust_full_name as username,
		s.ust_text as tweet,
		s.ust_created_at as publicationDate,
		s.ust_api_document as Json,
		MAX(COALESCE(p.total_retweets, h.total_retweets)) retweets,
		MAX(COALESCE(p.total_favorites, h.total_favorites)) favorites,
		s.ust_id as id,
		s.ust_status_id as statusId,
		h.is_retweet,
		s.ust_created_at as checkedAt
    `

	fromClause := `
		FROM highlight h
		INNER JOIN weaving_status s
		ON s.ust_id = h.status_id
		` + sinceWhen() + `
		` + constraintOnRetweetStatus + `
		INNER JOIN publishers_list
		ON h.aggregate_id = publishers_list.id
		AND (
			publishers_list.public_id = $2
			OR publishers_list.public_id = $3
		)
	`

	whereClause := `
	-- Prevent publications by deleted members from being fetched
	WHERE 
	(h.publication_date_time::timestamp - '1 HOUR'::interval)::date = $4 ` +
	constraintOnRetweetStatus + `
	AND h.member_id NOT IN (
		SELECT usr_id
		FROM weaving_user member,
		publishers_list publication_list
		WHERE publication_list.deleted_at IS NOT NULL
		AND member.usr_twitter_username = publication_list.screen_name
		AND publication_list.screen_name IS NOT NULL
	) 
	`

    groupByClause := `
		GROUP BY 
		h.status_id,
		CONCAT('https://twitter.com/', ust_full_name, '/status/', ust_status_id),
		s.ust_full_name,
		s.ust_text,
		s.ust_created_at,
		s.ust_api_document,
		s.ust_id,
		h.is_retweet,
		s.ust_created_at
    `
	if distinctSources {
		selectClause = `
			SELECT
			(ARRAY_AGG(CONCAT('https://twitter.com/', ust_full_name, '/status/', ust_status_id) ORDER BY COALESCE(p.total_retweets, h.total_retweets, (ust_api_document::json->>'retweet_count')::integer) DESC))[1] as url,
			(ARRAY_AGG(s.ust_full_name ORDER BY COALESCE(p.total_retweets, h.total_retweets, (ust_api_document::json->>'retweet_count')::integer) DESC))[1] as username,
			(ARRAY_AGG(s.ust_text ORDER BY COALESCE(p.total_retweets, h.total_retweets, (ust_api_document::json->>'retweet_count')::integer) DESC))[1] as tweet,
			(ARRAY_AGG(s.ust_created_at ORDER BY COALESCE(p.total_retweets, h.total_retweets, (ust_api_document::json->>'retweet_count')::integer) DESC))[1] as publicationDate,
			(ARRAY_AGG(s.ust_api_document ORDER BY COALESCE(p.total_retweets, h.total_retweets, (ust_api_document::json->>'retweet_count')::integer) DESC))[1] as Json,
			MAX(COALESCE(p.total_retweets, h.total_retweets, (ust_api_document::json->>'retweet_count')::integer)) retweets,
			MAX(COALESCE(p.total_favorites, h.total_retweets, (ust_api_document::json->>'favorite_count')::integer)) favorites,
			(ARRAY_AGG(s.ust_id ORDER BY COALESCE(p.total_retweets, h.total_retweets, (ust_api_document::json->>'retweet_count')::integer) DESC))[1] as id,
			(ARRAY_AGG(s.ust_status_id ORDER BY COALESCE(p.total_retweets, h.total_retweets, (ust_api_document::json->>'retweet_count')::integer) DESC))[1] as statusId,
			(ARRAY_AGG(COALESCE(h.is_retweet, s.ust_api_document::json->>'retweeted_status_result' IS NOT NULL, false) ORDER BY COALESCE(p.total_retweets, h.total_retweets, (ust_api_document::json->>'retweet_count')::integer) DESC))[1] as is_retweet,
			(ARRAY_AGG(s.ust_created_at ORDER BY COALESCE(p.total_retweets, h.total_retweets, (ust_api_document::json->>'retweet_count')::integer) DESC))[1] as checkedAt
		`

		fromClause = `
			FROM weaving_status s
			LEFT JOIN highlight h
			ON s.ust_id = h.status_id
		    ` + sinceWhen() + `
		    ` + constraintOnRetweetStatus + `
			INNER JOIN publishers_list
			ON (
				h.aggregate_id = publishers_list.id
				OR (
					s.ust_full_name = publishers_list.screen_name
					AND publishers_list.screen_name IS NOT NULL
				)
			) AND (
				publishers_list.public_id = $2
				OR publishers_list.public_id = $3
			)
		`

		isOfRetweetKind := "true"
		if !includeRetweets {
			isOfRetweetKind = "false"
		}

		whereClause = `
			WHERE
			(s.ust_created_at::timestamp - '1 HOUR'::interval)::date = $4
			AND COALESCE(h.is_retweet, s.ust_api_document::json->>'retweeted_status_result' IS NOT NULL, false) =  ` + isOfRetweetKind + `
			AND ((s.ust_api_document::json->>'user')::json->>'id_str')::bigint NOT IN (
				SELECT usr_twitter_id::bigint
				FROM weaving_user member,
				publishers_list publication_list
				WHERE publication_list.deleted_at IS NOT NULL
				AND member.usr_twitter_username = publication_list.screen_name
				AND publication_list.screen_name IS NOT NULL
			)
		`

		groupByClause = `
			GROUP BY 
			s.ust_full_name
		`
	}

	var query string
	query = selectClause + fromClause + `
		LEFT JOIN status_popularity p
		ON p.status_id = h.status_id
		AND (p.checked_at::timestamp - '1 HOUR'::interval)::date = (h.publication_date_time::timestamp - '1 HOUR'::interval)::date ` +
		whereClause +
		groupByClause + `
		ORDER BY retweets ` + sortingOrder

	if limit > 0 {
		query = query + ` OFFSET $5 LIMIT $6`
	}

	if dryMode {
		fmt.Printf("```SQL\n")
		fmt.Printf("%s\n", query)
		fmt.Printf("```")
	}

	selectTweets, err := db.Prepare(query)
	handleError(err)

	rows := selectTweetsWindow(limit, page, selectTweets, publishersListId, err)

	migrateStatusesToFirebaseApp(rows, firebase, publishersListId, distinctSources, includeRetweets, totalHighlights)
}

func selectTweetsWindow(limit int, page int, selectTweets *sql.Stmt, publishersListId string, err error) *sql.Rows {
	if limit > 0 {
		offset := page * tweetPerPage
		itemsPerPage := limit

		if dryMode {
			fmt.Printf("```SQL\n")
			fmt.Printf("%s\n", sinceDate)
			fmt.Printf("%s\n", publishersListId)
			fmt.Printf("%s\n", deprecatedPublisherId)
			fmt.Printf("%s\n", sinceDate)
			fmt.Printf("%d\n", offset)
			fmt.Printf("%d\n", itemsPerPage)
			fmt.Printf("```")
		}

		rows, err := selectTweets.Query(sinceDate, publishersListId, deprecatedPublisherId, sinceDate, offset, itemsPerPage)
		handleError(err)

		return rows
	}

	rows, err := selectTweets.Query(sinceDate, publishersListId, deprecatedPublisherId, sinceDate)
	handleError(err)

	return rows
}

func countHighlights(db *sql.DB, distinctSources bool, limit int) int {
	var totalHighlights int

	fromClause := `
		FROM highlight h
		INNER JOIN weaving_status s
		ON  s.ust_id = h.status_id
		` + sinceWhen() + ` 
		INNER JOIN publishers_list
		ON h.aggregate_id = publishers_list.id
		AND ( 
			publishers_list.public_id = $2 OR 
			publishers_list.public_id = $3
		)
	`

	whereClause := `
		WHERE
		(h.publication_date_time::timestamp - '1 HOUR'::interval)::date = $4
	`

	if distinctSources {

		fromClause = `
			FROM weaving_status s
			LEFT JOIN highlight h
			ON s.ust_id = h.status_id
			` + sinceWhen() + ` 
			LEFT JOIN publishers_list
			ON h.aggregate_id = publishers_list.id
			AND ( 
				publishers_list.public_id = $2 OR 
				publishers_list.public_id = $3
			)
		`

		whereClause = `
			WHERE
			(s.ust_created_at::timestamp - '1 HOUR'::interval)::date = $4
		`
	}

	var query string
	query = `
		SELECT COUNT(*) highlights ` +
		fromClause + `
		LEFT JOIN status_popularity p
		ON p.status_id = h.status_id
		AND (p.checked_at::timestamp - '1 HOUR'::interval)::date = (h.publication_date_time::timestamp - '1 HOUR'::interval)::date ` +
		whereClause

	statement, err := db.Prepare(query)
	handleError(err)

	highlightsCount, err := statement.Query(sinceDate, publishersListId, deprecatedPublisherId, sinceDate)
	handleError(err)

	columns, err := highlightsCount.Columns()
	handleError(err)
	values := make([]sql.RawBytes, len(columns))
	scanArgs := make([]interface{}, len(values))
	scanArgs[0] = &values[0]

	highlightsCount.Next()
	err = highlightsCount.Scan(scanArgs...)
	handleError(err)

	for _, col := range values {
		totalHighlights, err = strconv.Atoi(string(col))
		handleError(err)
	}

	fmt.Printf("Found %d matching highlights on %s\n", totalHighlights, sinceDate)

	if limit > -1 && limit < totalHighlights {
		return limit
	}

	return totalHighlights
}

func sinceWhen() string {
	if sinceAWeekAgo {
		return `AND s.ust_created_at::timestamp - '1 HOUR'::interval > NOW()::now - '7 DAYS::interval'`
	}

	return `
		AND (s.ust_created_at::timestamp - '1 HOUR'::interval)::date = (h.publication_date_time::timestamp - '1 HOUR'::interval)::date
		AND (s.ust_created_at::timestamp - '1 HOUR'::interval)::date = $1
	`
}

func migrateStatusesToFirebaseApp(
	rows *sql.Rows,
	firebase *firego.Firebase,
	publishersListId string,
	distinctSources bool,
	includeRetweets bool,
	totalHighlights int) {
	columns, err := rows.Columns()
	handleError(err)
	values := make([]sql.RawBytes, len(columns))
	scanArgs := make([]interface{}, len(values))
	for i := range values {
		scanArgs[i] = &values[i]
	}

	rowIndex := 0

	var tweets []Tweet

	if migrateDistinctSourcesOnly {
		tweets = make([]Tweet, 10)
	} else {
		tweets = make([]Tweet, totalHighlights)
	}

	for rows.Next() {
		err = rows.Scan(scanArgs...)
		handleError(err)

		var decodedApiDocument Status
		var value string
		var tweet Tweet

		tweet.canBeRetweet = includeRetweets

		for i, col := range values {
			value = string(col)

			switch {
			case i == 0:
				tweet.url = value
			case i == 1:
				tweet.username = value
			case i == 2:
				tweet.tweet = value
			case i == 3:
				tweet.publishedAt = value
			case i == 4:
				tweet.json = value
			case i == 5:
				tweet.retweets, err = strconv.Atoi(value)
			case i == 6:
				tweet.favorites, err = strconv.Atoi(value)
			case i == 7:
				tweet.id, err = strconv.Atoi(value)
			case i == 8:
				tweet.twitterId = value
			case i == 9:
				tweet.isRetweet = false
				if value == "1" {
					tweet.isRetweet = true
				}
			case i == 10:
				tweet.checkedAt = value
			}

			tweets[rowIndex] = tweet

			if quiet {
				continue
			}

			if i != 4 && i != 1 {
				fmt.Printf("%s: %s\n", columns[i], value)
			} else if i == 4 {
				apiDocument := []byte(value)

				isValid := json.Valid(apiDocument)
				if !isValid {
					fmt.Printf("%s for status \"%s\"", "Invalid JSON", string(values[8]))
					continue
				}

				err := json.Unmarshal(apiDocument, &decodedApiDocument)
				if err != nil {
					handleError(err)
				}
			}
		}

		if quiet && rowIndex%1000 == 0 {
			fmt.Printf(`.`)
		}

		rowIndex++

		if !quiet {
			fmt.Println("------------------")
		}
	}

	fmt.Printf("\n")

	var statusType string
	statusType = "status"

	if includeRetweets {
		statusType = "retweet"
	}

	if distinctSources {
		statusType = statusType + "FromDistinctSources"
	}

	path := "highlights/" + publishersListId + "/" + sinceDate + "/" + statusType + "/"
	fmt.Printf("About to remove %s\n", path)
	statusRef, err := firebase.Ref(path)
	handleError(err)

	err = statusRef.Remove()
	handleError(err)

	if parallel {
		swg := sizedwaitgroup.New(100)

		for index, tweet := range tweets {
			swg.Add()

			go (func(tweet Tweet, index int) {
				defer swg.Done()
				addToFirebaseApp(tweet, index, distinctSources, firebase, publishersListId)
			})(tweet, index)
		}

		swg.Wait()

		return
	}

	for index, tweet := range tweets {
		addToFirebaseApp(tweet, index, distinctSources, firebase, publishersListId)
	}
}

func addToFirebaseApp(tweet Tweet, index int, distinctSources bool, firebase *firego.Firebase, publishersListId string) {
	var decodedApiDocument Status
	apiDocument := []byte(tweet.json)

	isValid := json.Valid(apiDocument)
	if !isValid {
		fmt.Printf("%s for status \"%s\"", "Invalid JSON", tweet.twitterId)
		return
	}

	err := json.Unmarshal(apiDocument, &decodedApiDocument)
	handleError(err)

	statusId := decodedApiDocument.Id

	var statusType string
	statusType = "status"

	if tweet.canBeRetweet {
		statusType = "retweet"
	}

	if distinctSources {
		statusType = statusType + "FromDistinctSources"
	}

	statusRef, err := firebase.Ref("highlights/" + publishersListId + "/" + sinceDate + "/" +
		statusType + "/" + statusId)
	handleError(err)

	status := map[string]interface{}{
		"id":             tweet.id,
		"twitterId":      tweet.twitterId,
		"username":       tweet.username,
		"text":           tweet.tweet,
		"url":            tweet.url,
		"json":           tweet.json,
		"publishedAt":    tweet.publishedAt,
		"checkedAt":      tweet.checkedAt,
		"isRetweet":      tweet.isRetweet,
		"twitter_id":     decodedApiDocument.Id,
		"totalRetweets":  tweet.retweets,
		"totalFavorites": tweet.favorites,
	}

	if dryMode {
		fmt.Printf("Dry mode is active. Exiting.\n")

		return
	}

	err = statusRef.Update(status)
	if err != nil {
		fmt.Printf("%s \"%s\" indexed at %d\n", "Could not migrate status", tweet.twitterId, index)
		fmt.Printf("(%s)", err)
		return
	}

	fmt.Printf("%s \"%s\" indexed at %d\n", "Migrated status", tweet.twitterId, index)
}

func handleError(err error) {
	if err != nil {
		log.Fatal(err.Error())
	}
}
package search

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"openreplay/backend/pkg/db/postgres/pool"
	"sort"
	"slices"
	"strconv"
	"strings"
	"time"

	"openreplay/backend/pkg/analytics/charts"
	"openreplay/backend/pkg/analytics/lexicon"
	"openreplay/backend/pkg/analytics/model"

	"openreplay/backend/pkg/logger"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/lib/pq"
)

type Search interface {
	GetAll(ctx context.Context, projectId int, userId uint64, req *model.SessionsSearchRequest) (interface{}, error)
	GetBookmarkedSessions(ctx context.Context, projectId int, userId uint64, req *model.SessionsSearchRequest) (interface{}, error)
	GetSessionIds(ctx context.Context, projectId int, userId uint64, req *model.SessionsSearchRequest) (interface{}, error)
	GetCounts(ctx context.Context, projectId int, req *model.SessionsSearchRequest) (sessions, users int64, err error)
}

type searchImpl struct {
	chConn     driver.Conn
	pgConn     pool.Pool
	Logger     logger.Logger
	filterRefs []lexicon.FilterRef
}

func New(logger logger.Logger, chConn driver.Conn, pgConn pool.Pool, segments lexicon.Segments) (Search, error) {
	return &searchImpl{
		chConn: chConn,
		pgConn: pgConn,
		Logger: logger,
		filterRefs: []lexicon.FilterRef{
			lexicon.NewSegmentFilterRef(segments),
		},
	}, nil
}

var sortOptions = map[string]string{
	"datetime":    "s.datetime",
	"startTs":     "s.datetime",
	"eventsCount": "s.events_count",
}

const (
	sessionsQuery = `
SELECT DISTINCT ON (session_id)
	toString(s.session_id)                    AS session_id,
	s.project_id,
	toUInt64(toUnixTimestamp(s.datetime)*1000) AS start_ts,
	s.duration,
	s.platform,
	s.timezone,
	s.user_id,
	s.user_uuid,
	s.user_anonymous_id,
	s.user_browser,
	s.user_city,
	s.user_country,
	s.user_device,
	s.user_device_type,
	s.user_os,
	s.user_state,
	s.screen_width,
	s.screen_height,
	greatest(s.events_count,1) AS events_count,
	viewed_sessions.session_id>0 AS viewed,
	count(DISTINCT session_id) OVER() AS total_number_of_sessions
	%s
FROM experimental.sessions AS s
	%s
	%s
	%s
WHERE %s
ORDER BY %s %s
LIMIT %d OFFSET %d;`
	groupedSessionsSourceQuery = `
SELECT DISTINCT ON (session_id)
	toString(s.session_id) AS session_id,
	toUInt64(toUnixTimestamp(s.datetime)*1000) AS start_ts,
	s.duration,
	s.user_id,
	s.user_uuid,
	s.user_anonymous_id,
	greatest(s.events_count,1) AS events_count
FROM experimental.sessions AS s
	%s
	%s
WHERE %s
ORDER BY s.datetime ASC;`
	viewedSessionsJoinTemplate = `ANY LEFT JOIN (
	SELECT DISTINCT session_id
	FROM experimental.user_viewed_sessions
	WHERE user_id    = %d
	  AND project_id = %d
	  AND _timestamp >= toDateTime(%d)
) AS viewed_sessions ON (viewed_sessions.session_id=s.session_id)`
)

// GetAll retrieves sessions based on the request parameters.
// Returns different response types based on whether series are requested:
//
// Regular request (req.Series is empty):
//   - Returns *model.GetSessionsResponse with total count and sessions array
//   - Uses req.Filters and req.EventsOrder for filtering
//
// Series request (req.Series has elements):
//   - Returns *model.SeriesSessionsResponse with grouped sessions by series
//   - Each series uses its own filters from req.Series[i].Filters
//   - Each series uses its own events order from req.Series[i].EventsOrder
//   - Pagination is applied to each series individually
//   - Response includes series index, total count, and sessions for each series
func (s *searchImpl) GetAll(ctx context.Context, projectId int, userId uint64, req *model.SessionsSearchRequest) (interface{}, error) {
	if req == nil {
		return nil, errors.New("nil request")
	}

	if err := lexicon.ResolveSessionSearch(ctx, s.filterRefs, uint32(projectId), req); err != nil {
		return nil, err
	}

	if len(req.Series) > 0 {
		return s.getSeriesSessions(projectId, userId, req)
	}

	if req.GroupByUser {
		return s.getGroupedSessions(projectId, userId, req)
	}

	return s.getSingleSessions(projectId, userId, req)
}

type sessionsQueryComponents struct {
	eventsInnerJoin string
	leftAntiJoin    string
	viewedJoin      string
	sessionsWhere   []string
	sortField       string
	sortOrder       string
	limit           int
	offset          int
}

func (s *searchImpl) buildSessionsQueryComponents(projectId int, userId uint64, req *model.SessionsSearchRequest) *sessionsQueryComponents {
	startSec := req.StartDate / 1000
	endSec := req.EndDate / 1000
	offset := (req.Page - 1) * req.Limit

	eventsWhere, filtersWhere, negativeEventsWhere, sessionsWhere := charts.BuildWhere(req.Filters, req.EventsOrder, "e", "s")
	sessionsWhere = append([]string{fmt.Sprintf("s.project_id = %d", projectId),
		fmt.Sprintf("s.datetime BETWEEN toDateTime(%d) AND toDateTime(%d)", startSec, endSec),
	}, sessionsWhere...)

	var eventsInnerJoin string
	var leftAntiJoin string

	conds := make([]string, 0)

	if len(eventsWhere) > 0 || len(filtersWhere) > 0 {
		conds = append([]string{
			fmt.Sprintf("e.project_id = %d", projectId),
			fmt.Sprintf("e.created_at BETWEEN toDateTime(%d) AND toDateTime(%d)", startSec, endSec),
		}, conds...)
		conds = append(conds, filtersWhere...)

		if len(eventsWhere) == 1 {
			conds = append(conds, eventsWhere[0])
		}

		joinClause := charts.BuildJoinClause(req.EventsOrder, eventsWhere)
		eventsInnerJoin = fmt.Sprintf(`ANY INNER JOIN (
		SELECT DISTINCT session_id
		FROM product_analytics.events AS e
		WHERE %s
		%s
	) AS fs USING (session_id)`,
			strings.Join(conds, " AND \n"), joinClause)
	}

	if len(negativeEventsWhere) > 0 {
		conds = append([]string{
			fmt.Sprintf("e.project_id = %d", projectId),
			fmt.Sprintf("e.created_at BETWEEN toDateTime(%d) AND toDateTime(%d)", startSec, endSec),
		}, conds...)
		conds = append(conds, negativeEventsWhere...)

		leftAntiJoin = fmt.Sprintf(`LEFT ANTI JOIN (
		SELECT DISTINCT session_id
		FROM product_analytics.events AS e
		WHERE %s
	) AS negative_sessions USING (session_id)`,
			strings.Join(conds, " AND \n"))
	}

	sortField := sortOptions[req.Sort]
	if sortField == "" {
		sortField = sortOptions["datetime"]
	}
	sortOrder := "DESC"
	if strings.EqualFold(req.Order, "ASC") {
		sortOrder = "ASC"
	}

	viewedJoin := fmt.Sprintf(viewedSessionsJoinTemplate, userId, projectId, startSec)

	return &sessionsQueryComponents{
		eventsInnerJoin: eventsInnerJoin,
		leftAntiJoin:    leftAntiJoin,
		viewedJoin:      viewedJoin,
		sessionsWhere:   sessionsWhere,
		sortField:       sortField,
		sortOrder:       sortOrder,
		limit:           req.Limit,
		offset:          offset,
	}
}

func (s *searchImpl) getSingleSessions(projectId int, userId uint64, req *model.SessionsSearchRequest) (*model.GetSessionsResponse, error) {
	qc := s.buildSessionsQueryComponents(projectId, userId, req)

	var metasMap map[string]string = s.getMetadataColumns(projectId)
	var metas string = ""
	if len(metasMap) > 0 {
		metas = "," + strings.Join(slices.Collect(maps.Keys(metasMap)), ",")
	}
	query := fmt.Sprintf(sessionsQuery,
		metas,
		qc.eventsInnerJoin,
		qc.leftAntiJoin,
		qc.viewedJoin,
		strings.Join(qc.sessionsWhere, " AND "),
		qc.sortField,
		qc.sortOrder,
		qc.limit,
		qc.offset,
	)

	resp := &model.GetSessionsResponse{Sessions: make([]model.Session, 0)}
	_start := time.Now()
	if err := s.chConn.Select(context.Background(), &resp.Sessions, query); err != nil {
		if time.Since(_start) > 2*time.Second {
			s.Logger.Warn(context.Background(), "Slow getSingleSession select: %s", query)
		}
		s.Logger.Warn(context.Background(), "Error executing query: %s\nQuery: %s", err, query)
		return nil, err
	}
	if len(resp.Sessions) > 0 {
		resp.Total = resp.Sessions[0].TotalNumberOfSessions
	}
	processSessionsMetadata(resp.Sessions, metasMap)
	return resp, nil
}


func groupingIdentity(session model.Session) string {
	// Explicit application user identity is authoritative when available.
	if session.UserId != "" {
		return "user:" + session.UserId
	}
	// user_uuid is the tracker fingerprint persisted in browser localStorage.
	// Partition anonymous sessions by it before applying the time window so
	// two different visitors can never merge merely because they overlap.
	if session.UserUuid != "" {
		return "fingerprint:" + session.UserUuid
	}
	if session.UserAnonymousId != nil && *session.UserAnonymousId != "" {
		return "anonymous:" + *session.UserAnonymousId
	}
	// No identity evidence: fail closed and keep the session isolated.
	return "session:" + session.SessionId
}

func sessionEndTs(session model.Session) uint64 {
	return session.StartTs + uint64(session.Duration)
}

func reverseSessions(sessions []model.Session) {
	for i, j := 0, len(sessions)-1; i < j; i, j = i+1, j-1 {
		sessions[i], sessions[j] = sessions[j], sessions[i]
	}
}

func groupNearbySessions(sessions []model.Session, windowMinutes int, sortBy, order string) []model.SessionGroup {
	if windowMinutes <= 0 {
		windowMinutes = 120
	}
	windowMs := uint64(windowMinutes) * 60 * 1000

	byIdentity := make(map[string][]model.Session)
	for _, session := range sessions {
		key := groupingIdentity(session)
		byIdentity[key] = append(byIdentity[key], session)
	}

	groups := make([]model.SessionGroup, 0)
	for _, userSessions := range byIdentity {
		sort.SliceStable(userSessions, func(i, j int) bool {
			if userSessions[i].StartTs == userSessions[j].StartTs {
				return userSessions[i].SessionId < userSessions[j].SessionId
			}
			return userSessions[i].StartTs < userSessions[j].StartTs
		})

		for _, session := range userSessions {
			endTs := sessionEndTs(session)
			if len(groups) == 0 || groupingIdentity(groups[len(groups)-1].Sessions[0]) != groupingIdentity(session) ||
				session.StartTs > groups[len(groups)-1].EndTs+windowMs {
				groups = append(groups, model.SessionGroup{
					GroupId:     "group-" + session.SessionId,
					StartTs:     session.StartTs,
					EndTs:       endTs,
					EventsCount: uint64(session.EventsCount),
					Sessions:    []model.Session{session},
				})
				continue
			}

			group := &groups[len(groups)-1]
			group.Sessions = append(group.Sessions, session)
			group.EventsCount += uint64(session.EventsCount)
			if endTs > group.EndTs {
				group.EndTs = endTs
			}
		}
	}

	desc := !strings.EqualFold(order, "asc")
	sort.SliceStable(groups, func(i, j int) bool {
		if sortBy == "eventsCount" {
			if groups[i].EventsCount != groups[j].EventsCount {
				if desc {
					return groups[i].EventsCount > groups[j].EventsCount
				}
				return groups[i].EventsCount < groups[j].EventsCount
			}
			if desc {
				return groups[i].EndTs > groups[j].EndTs
			}
			return groups[i].StartTs < groups[j].StartTs
		}

		if desc {
			if groups[i].EndTs != groups[j].EndTs {
				return groups[i].EndTs > groups[j].EndTs
			}
			return groups[i].StartTs > groups[j].StartTs
		}
		if groups[i].StartTs != groups[j].StartTs {
			return groups[i].StartTs < groups[j].StartTs
		}
		return groups[i].EndTs < groups[j].EndTs
	})

	if desc {
		for i := range groups {
			reverseSessions(groups[i].Sessions)
		}
	}

	return groups
}

func groupPage(groups []model.SessionGroup, page, limit int) ([]model.SessionGroup, int) {
	total := len(groups)
	start := (page - 1) * limit
	if start < 0 {
		start = 0
	}
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	return groups[start:end], total
}

func groupSessionIDs(groups []model.SessionGroup) ([]uint64, error) {
	ids := make([]uint64, 0)
	for _, group := range groups {
		for _, session := range group.Sessions {
			id, err := strconv.ParseUint(session.SessionId, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid session id %q: %w", session.SessionId, err)
			}
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func hydrateSessionGroups(groups []model.SessionGroup, sessions []model.Session) []model.SessionGroup {
	byID := make(map[string]model.Session, len(sessions))
	for _, session := range sessions {
		byID[session.SessionId] = session
	}
	for gi := range groups {
		for si := range groups[gi].Sessions {
			if session, ok := byID[groups[gi].Sessions[si].SessionId]; ok {
				groups[gi].Sessions[si] = session
			}
		}
	}
	return groups
}

func (s *searchImpl) getGroupedSessions(projectId int, userId uint64, req *model.SessionsSearchRequest) (*model.GetSessionsResponse, error) {
	qc := s.buildSessionsQueryComponents(projectId, userId, req)

	// Phase 1 intentionally selects only fields needed to group and sort. This
	// keeps wide metadata/device rows out of the all-matching-sessions scan.
	sourceQuery := fmt.Sprintf(groupedSessionsSourceQuery,
		qc.eventsInnerJoin,
		qc.leftAntiJoin,
		strings.Join(qc.sessionsWhere, " AND "),
	)

	lightSessions := make([]model.Session, 0)
	started := time.Now()
	if err := s.chConn.Select(context.Background(), &lightSessions, sourceQuery); err != nil {
		s.Logger.Warn(context.Background(), "Error executing grouped sessions source query: %s\nQuery: %s", err, sourceQuery)
		return nil, err
	}
	if time.Since(started) > 2*time.Second {
		s.Logger.Warn(context.Background(), "Slow grouped sessions source select: %s", sourceQuery)
	}

	allGroups := groupNearbySessions(lightSessions, req.GroupWindowMinutes, req.Sort, req.Order)
	pageGroups, total := groupPage(allGroups, req.Page, req.Limit)
	if len(pageGroups) == 0 {
		return &model.GetSessionsResponse{
			Total:    uint64(total),
			Sessions: make([]model.Session, 0),
			Groups:   make([]model.SessionGroup, 0),
		}, nil
	}

	// Phase 2 hydrates only members of groups visible on the current page.
	ids, err := groupSessionIDs(pageGroups)
	if err != nil {
		return nil, err
	}
	idParts := make([]string, len(ids))
	for i, id := range ids {
		idParts[i] = strconv.FormatUint(id, 10)
	}

	metasMap := s.getMetadataColumns(projectId)
	metas := ""
	if len(metasMap) > 0 {
		metas = "," + strings.Join(slices.Collect(maps.Keys(metasMap)), ",")
	}
	detailsWhere := fmt.Sprintf(
		"s.project_id = %d AND s.session_id IN (%s)",
		projectId,
		strings.Join(idParts, ","),
	)
	detailsQuery := fmt.Sprintf(sessionsQuery,
		metas,
		"",
		"",
		qc.viewedJoin,
		detailsWhere,
		"s.datetime",
		"ASC",
		len(ids),
		0,
	)

	fullSessions := make([]model.Session, 0, len(ids))
	detailsStarted := time.Now()
	if err := s.chConn.Select(context.Background(), &fullSessions, detailsQuery); err != nil {
		s.Logger.Warn(context.Background(), "Error hydrating grouped session page: %s\nQuery: %s", err, detailsQuery)
		return nil, err
	}
	if time.Since(detailsStarted) > 2*time.Second {
		s.Logger.Warn(context.Background(), "Slow grouped session hydration select: %s", detailsQuery)
	}
	processSessionsMetadata(fullSessions, metasMap)
	pageGroups = hydrateSessionGroups(pageGroups, fullSessions)

	return &model.GetSessionsResponse{
		Total:    uint64(total),
		Sessions: make([]model.Session, 0),
		Groups:   pageGroups,
	}, nil
}

func (s *searchImpl) getSeriesSessions(projectId int, userId uint64, req *model.SessionsSearchRequest) (*model.SeriesSessionsResponse, error) {
	startSec := req.StartDate / 1000
	endSec := req.EndDate / 1000
	offset := (req.Page - 1) * req.Limit

	response := &model.SeriesSessionsResponse{
		Series: make([]model.SeriesSessionData, 0, len(req.Series)),
	}
	var metasMap map[string]string
	for i, series := range req.Series {
		// Create a copy of the request with series-specific filters
		seriesReq := &model.SessionsSearchRequest{
			Filters:     series.Filter.Filters,
			Sort:        req.Sort,
			Order:       req.Order,
			EventsOrder: string(series.Filter.EventsOrder),
			Limit:       req.Limit,
		}

		eventsWhere, filtersWhere, _, sessionsWhere := charts.BuildWhere(seriesReq.Filters, seriesReq.EventsOrder, "e", "s")
		sessionsWhere = append([]string{fmt.Sprintf("s.project_id = %d", projectId),
			fmt.Sprintf("s.datetime BETWEEN toDateTime(%d) AND toDateTime(%d)", startSec, endSec),
		}, sessionsWhere...)

		var eventsInnerJoin string

		conds := make([]string, 0, len(seriesReq.Filters)+2)

		if len(eventsWhere) > 0 || len(filtersWhere) > 0 {
			conds = append([]string{
				fmt.Sprintf("e.project_id = %d", projectId),
				fmt.Sprintf("e.created_at BETWEEN toDateTime(%d) AND toDateTime(%d)", startSec, endSec),
			}, conds...)
			conds = append(conds, filtersWhere...)

			if len(eventsWhere) == 1 {
				conds = append(conds, eventsWhere[0])
			}

			joinClause := charts.BuildJoinClause(seriesReq.EventsOrder, eventsWhere)
			eventsInnerJoin = fmt.Sprintf(`ANY INNER JOIN (
			SELECT DISTINCT session_id
			FROM product_analytics.events AS e
			WHERE %s
			%s
		) AS fs USING (session_id)`,
				strings.Join(conds, " AND \n"), joinClause)
		}

		sortField := sortOptions[seriesReq.Sort]
		if sortField == "" {
			sortField = sortOptions["datetime"]
		}
		sortOrder := "DESC"
		if strings.EqualFold(seriesReq.Order, "ASC") {
			sortOrder = "ASC"
		}

		viewedJoin := fmt.Sprintf(viewedSessionsJoinTemplate, userId, projectId, startSec)
		metasMap = s.getMetadataColumns(projectId)
		var metas string = ""
		if len(metasMap) > 0 {
			metas = "," + strings.Join(slices.Collect(maps.Keys(metasMap)), ",")
		}
		query := fmt.Sprintf(sessionsQuery,
			metas,
			eventsInnerJoin,
			"", //LEFT ANTI JOIN not supported in series context yet
			viewedJoin,
			strings.Join(sessionsWhere, " AND "),
			sortField,
			sortOrder,
			seriesReq.Limit,
			offset,
		)

		seriesData := model.SeriesSessionData{
			SeriesId:   series.SeriesID,
			SeriesName: series.Name,
			Sessions:   make([]model.Session, 0),
		}

		_start := time.Now()
		if err := s.chConn.Select(context.Background(), &seriesData.Sessions, query); err != nil {
			if time.Since(_start) > 2*time.Second {
				s.Logger.Warn(context.Background(), "Slow getSeriesSessions [series %d]: %s", i, query)
			}
			s.Logger.Error(context.Background(), "Error executing query: %s\nQuery: %s", err, query)
			return nil, err
		}
		if len(seriesData.Sessions) > 0 {
			seriesData.Total = seriesData.Sessions[0].TotalNumberOfSessions
		}
		response.Series = append(response.Series, seriesData)
	}
	processSessionsMetadata(response.Series[0].Sessions, metasMap)
	return response, nil
}

func (s *searchImpl) getMetadataColumns(projectId int) map[string]string {
	row, err := s.pgConn.Query(`
SELECT metadata_1, metadata_2, metadata_3, metadata_4, metadata_5,
metadata_6, metadata_7, metadata_8, metadata_9, metadata_10
FROM projects
WHERE project_id = $1;`, projectId)
	if err != nil {
		return nil
	}
	defer row.Close()
	// Move to first row
	if !row.Next() {
		s.Logger.Debug(context.Background(), "No rows found")
		return make(map[string]string)
	}
	// Get values
	values, err := row.Values()
	if err != nil {
		s.Logger.Error(context.Background(), "Error getting values: %v", err)
		return make(map[string]string)
	}
	// Get column names
	fields := row.FieldDescriptions()
	var result map[string]string = make(map[string]string)
	for i, field := range fields {
		if values[i] != nil {
			result[string(field.Name)] = values[i].(string)
		}
	}
	return result
}

func (s *searchImpl) GetBookmarkedSessions(ctx context.Context, projectId int, userId uint64, req *model.SessionsSearchRequest) (interface{}, error) {
	if req == nil {
		return nil, errors.New("nil request")
	}

	offset := (req.Page - 1) * req.Limit

	sortOrder := "DESC"
	if strings.EqualFold(req.Order, "ASC") {
		sortOrder = "ASC"
	}

	query := fmt.Sprintf(`
SELECT
	s.session_id::text AS session_id,
	s.project_id,
	s.start_ts,
	COALESCE(s.duration, 0) AS duration,
	s.platform,
	COALESCE(s.timezone, '') AS timezone,
	COALESCE(s.user_id, '') AS user_id,
	s.user_uuid::text,
	COALESCE(s.user_anonymous_id, '') AS user_anonymous_id,
	COALESCE(s.user_browser, '') AS user_browser,
	COALESCE(s.user_city, '') AS user_city,
	s.user_country,
	COALESCE(s.user_device, '') AS user_device,
	s.user_device_type,
	s.user_os,
	COALESCE(s.user_state, '') AS user_state,
	greatest(s.events_count,1) AS events_count,
	s.pages_count,
	ARRAY(SELECT unnest(s.issue_types)::text) AS issue_types,
	true AS viewed,
	COUNT(*) OVER() AS total_number_of_sessions,
	s.metadata_1,
	s.metadata_2,
	s.metadata_3,
	s.metadata_4,
	s.metadata_5,
	s.metadata_6,
	s.metadata_7,
	s.metadata_8,
	s.metadata_9,
	s.metadata_10
FROM public.user_favorite_sessions b
INNER JOIN public.sessions s ON s.session_id = b.session_id
WHERE b.user_id = $1
  AND s.project_id = $2
ORDER BY s.start_ts %s
LIMIT $3 OFFSET $4`,
		sortOrder,
	)

	start := time.Now()
	rows, err := s.pgConn.Query(query, userId, projectId, req.Limit, offset)
	duration := time.Since(start)
	if duration > 2*time.Second {
		s.Logger.Warn(context.Background(), "Slow bookmarked query (%.2fs): %s", duration.Seconds(), query)
	}
	if err != nil {
		s.Logger.Error(context.Background(), "Error executing bookmarked sessions query: %s\nQuery: %s", err, query)
		return nil, err
	}
	defer rows.Close()

	resp := &model.GetSessionsResponse{Sessions: make([]model.Session, 0)}
	metasMap := s.getMetadataColumns(projectId)

	for rows.Next() {
		var session model.Session
		err := rows.Scan(
			&session.SessionId,
			&session.ProjectId,
			&session.StartTs,
			&session.Duration,
			&session.Platform,
			&session.Timezone,
			&session.UserId,
			&session.UserUuid,
			&session.UserAnonymousId,
			&session.UserBrowser,
			&session.UserCity,
			&session.UserCountry,
			&session.UserDevice,
			&session.UserDeviceType,
			&session.UserOs,
			&session.UserState,
			&session.EventsCount,
			&session.PagesCount,
			pq.Array(&session.IssueTypes),
			&session.Viewed,
			&session.TotalNumberOfSessions,
			&session.Metadata1,
			&session.Metadata2,
			&session.Metadata3,
			&session.Metadata4,
			&session.Metadata5,
			&session.Metadata6,
			&session.Metadata7,
			&session.Metadata8,
			&session.Metadata9,
			&session.Metadata10,
		)
		if err != nil {
			s.Logger.Error(context.Background(), "Error scanning row: %s", err)
			return nil, err
		}

		resp.Sessions = append(resp.Sessions, session)
	}

	if len(resp.Sessions) > 0 {
		resp.Total = resp.Sessions[0].TotalNumberOfSessions
	}

	processSessionsMetadata(resp.Sessions, metasMap)

	return resp, nil
}

func (s *searchImpl) GetCounts(ctx context.Context, projectId int, req *model.SessionsSearchRequest) (int64, int64, error) {
	if req == nil {
		return 0, 0, errors.New("nil request")
	}
	if err := lexicon.ResolveSessionSearch(ctx, s.filterRefs, uint32(projectId), req); err != nil {
		return 0, 0, err
	}

	startSec := req.StartDate / 1000
	endSec := req.EndDate / 1000

	eventsWhere, filtersWhere, negativeEventsWhere, sessionsWhere := charts.BuildWhere(req.Filters, req.EventsOrder, "e", "s")
	sessionsWhere = append([]string{
		fmt.Sprintf("s.project_id = %d", projectId),
		fmt.Sprintf("s.datetime BETWEEN toDateTime(%d) AND toDateTime(%d)", startSec, endSec),
	}, sessionsWhere...)

	hasEventFilters := len(eventsWhere) > 0 || len(filtersWhere) > 0

	var distinctIdJoin string
	if hasEventFilters {
		conds := []string{
			fmt.Sprintf("e.project_id = %d", projectId),
			fmt.Sprintf("e.created_at BETWEEN toDateTime(%d) AND toDateTime(%d)", startSec, endSec),
		}
		conds = append(conds, filtersWhere...)
		if len(eventsWhere) == 1 {
			conds = append(conds, eventsWhere[0])
		}
		groupBy := charts.BuildJoinClause(req.EventsOrder, eventsWhere)
		if groupBy == "" {
			groupBy = "GROUP BY session_id"
		}
		distinctIdJoin = fmt.Sprintf(`ANY INNER JOIN (
		SELECT session_id, any(distinct_id) AS distinct_id
		FROM product_analytics.events AS e
		WHERE %s
		%s
	) AS fs USING (session_id)`,
			strings.Join(conds, " AND \n"), groupBy)
	} else {
		distinctIdJoin = fmt.Sprintf(`ANY LEFT JOIN (
		SELECT session_id, any(distinct_id) AS distinct_id
		FROM product_analytics.events
		WHERE project_id = %d
		  AND created_at BETWEEN toDateTime(%d) AND toDateTime(%d)
		GROUP BY session_id
	) AS fs USING (session_id)`, projectId, startSec, endSec)
	}

	var leftAntiJoin string
	if len(negativeEventsWhere) > 0 {
		negConds := []string{
			fmt.Sprintf("e.project_id = %d", projectId),
			fmt.Sprintf("e.created_at BETWEEN toDateTime(%d) AND toDateTime(%d)", startSec, endSec),
		}
		negConds = append(negConds, negativeEventsWhere...)
		leftAntiJoin = fmt.Sprintf(`LEFT ANTI JOIN (
		SELECT DISTINCT session_id
		FROM product_analytics.events AS e
		WHERE %s
	) AS negative_sessions USING (session_id)`, strings.Join(negConds, " AND \n"))
	}

	query := fmt.Sprintf(`
SELECT countDistinct(s.session_id) AS sessions_count,
       countDistinctIf(fs.distinct_id, fs.distinct_id != '') AS users_count
FROM experimental.sessions AS s
	%s
	%s
WHERE %s;`,
		distinctIdJoin,
		leftAntiJoin,
		strings.Join(sessionsWhere, " AND "),
	)

	var sessionsCount, usersCount uint64
	_start := time.Now()
	if err := s.chConn.QueryRow(ctx, query).Scan(&sessionsCount, &usersCount); err != nil {
		if time.Since(_start) > 2*time.Second {
			s.Logger.Warn(ctx, "Slow GetCounts query: %s", query)
		}
		s.Logger.Warn(ctx, "Error executing GetCounts query: %s\nQuery: %s", err, query)
		return 0, 0, err
	}
	return int64(sessionsCount), int64(usersCount), nil
}

func (s *searchImpl) GetSessionIds(ctx context.Context, projectId int, userId uint64, req *model.SessionsSearchRequest) (interface{}, error) {
	if req == nil {
		return nil, errors.New("nil request")
	}

	if err := lexicon.ResolveSessionSearch(ctx, s.filterRefs, uint32(projectId), req); err != nil {
		return nil, err
	}

	qc := s.buildSessionsQueryComponents(projectId, userId, req)

	query := fmt.Sprintf(`
SELECT
	toString(s.session_id) AS session_id
FROM experimental.sessions AS s
	%s
	%s
	%s
WHERE %s
ORDER BY %s %s
LIMIT %d OFFSET %d;`,
		qc.eventsInnerJoin,
		qc.leftAntiJoin,
		qc.viewedJoin,
		strings.Join(qc.sessionsWhere, " AND "),
		qc.sortField,
		qc.sortOrder,
		qc.limit,
		qc.offset,
	)

	_start := time.Now()
	sessionIds := make([]model.SessionIdData, 0)
	if err := s.chConn.Select(context.Background(), &sessionIds, query); err != nil {
		s.Logger.Error(context.Background(), "Error executing GetSessionIds query: %s\nQuery: %s", err, query)
		return nil, err
	}
	if time.Since(_start) > 2*time.Second {
		s.Logger.Warn(context.Background(), "Slow GetSessionIds select: %s", query)
	}
	return sessionIds, nil
}

func processSessionsMetadata(rows []model.Session, metasMap map[string]string) {
	if metasMap == nil {
		return
	}
	for i := range len(rows) {
		rows[i].Metadata = make(map[string]*string)
		for metaKey, columnName := range metasMap {
			var value *string
			switch metaKey {
			case "metadata_1":
				value = rows[i].Metadata1
			case "metadata_2":
				value = rows[i].Metadata2
			case "metadata_3":
				value = rows[i].Metadata3
			case "metadata_4":
				value = rows[i].Metadata4
			case "metadata_5":
				value = rows[i].Metadata5
			case "metadata_6":
				value = rows[i].Metadata6
			case "metadata_7":
				value = rows[i].Metadata7
			case "metadata_8":
				value = rows[i].Metadata8
			case "metadata_9":
				value = rows[i].Metadata9
			case "metadata_10":
				value = rows[i].Metadata10
			}
			rows[i].Metadata[columnName] = value
		}
	}
}

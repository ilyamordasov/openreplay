import React, { useEffect } from 'react';
import { FilterKey } from 'Types/filter/filterType';
import SessionItem from 'Shared/SessionItem';
import { NoContent, Loader, Pagination, Icon } from 'UI';
import { Button } from 'antd';
import { DownOutlined, RightOutlined } from '@ant-design/icons';
import { useLocation, useNavigate, withRouter } from 'App/routing';
import { session as sessionRoute, withSiteId } from 'App/routes';
import AnimatedSVG, { ICONS } from 'Shared/AnimatedSVG/AnimatedSVG';
import { numberWithCommas } from 'App/utils';
import RecordingStatus from 'Shared/SessionsTabOverview/components/RecordingStatus';
import { sessionService } from 'App/services';
import { observer } from 'mobx-react-lite';
import { useStore } from 'App/mstore';
import SessionDateRange from './SessionDateRange';
import { useTranslation } from 'react-i18next';
import { toast } from 'react-toastify';

type SessionStatus = {
  status: number;
  count: number;
};

const AUTO_REFRESH_INTERVAL = 5 * 60 * 1000;
let sessionTimeOut: any = null;
let sessionStatusTimeOut: any = null;

const STATUS_FREQUENCY = 5000;

function formatGroupTime(timestamp: number) {
  return new Date(timestamp).toLocaleString();
}

function NearbySessionGroup({
  group,
  sessionItemProps,
}: {
  group: any;
  sessionItemProps: (session: any) => Record<string, any>;
}) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const { projectsStore } = useStore();
  const [expanded, setExpanded] = React.useState(true);
  const [exporting, setExporting] = React.useState(false);
  const sessions = group.sessions ?? [];
  const first = sessions[0];
  const totalDurationMs = sessions.reduce(
    (sum: number, session: any) => sum + (session.durationMs ?? 0),
    0,
  );
  const totalMinutes = Math.max(1, Math.round(totalDurationMs / 60000));
  const orderedSessions = React.useMemo(
    () =>
      [...sessions].sort(
        (a: any, b: any) =>
          (a.startedAt ?? a.startTs ?? 0) - (b.startedAt ?? b.startTs ?? 0),
      ),
    [sessions],
  );
  const orderedSessionIds = orderedSessions.map((session: any) =>
    String(session.sessionId),
  );
  const canPlayMerged =
    orderedSessions.length > 1 &&
    orderedSessions.every(
      (session: any) => !['ios', 'android'].includes(session.platform),
    );

  const playMerged = () => {
    if (!canPlayMerged) return;
    const firstSessionId = orderedSessionIds[0];
    const target = withSiteId(
      sessionRoute(firstSessionId),
      projectsStore.siteId,
    );
    navigate(
      `${target}?stitched=${encodeURIComponent(orderedSessionIds.join(','))}`,
    );
  };

  const exportGroup = async () => {
    if (exporting || sessions.length === 0) return;
    setExporting(true);
    try {
      await sessionService.downloadSessionGroup(orderedSessionIds);
    } catch (error) {
      toast.error(
        error instanceof Error ? error.message : t('Failed to export sessions'),
      );
    } finally {
      setExporting(false);
    }
  };

  return (
    <div className="border-b">
      <div className="w-full flex items-center gap-3 px-4 py-3 bg-neutral-50 hover:bg-neutral-100">
        <button
          type="button"
          className="min-w-0 flex flex-1 items-center gap-3 text-left"
          onClick={() => setExpanded((value) => !value)}
        >
          <span className="text-neutral-500">
            {expanded ? <DownOutlined /> : <RightOutlined />}
          </span>
          <div className="min-w-0 flex-1">
            <div className="font-medium truncate">
              {first?.userDisplayName || t('Anonymous User')}
            </div>
            <div className="text-xs text-neutral-500">
              {formatGroupTime(group.startTs)} — {formatGroupTime(group.endTs)}
            </div>
          </div>
        </button>
        <Button
          size="small"
          disabled={!canPlayMerged}
          onClick={playMerged}
        >
          {t('Play merged')}
        </Button>
        <Button
          size="small"
          loading={exporting}
          disabled={exporting || sessions.length === 0}
          onClick={() => void exportGroup()}
        >
          {t('Export sessions')}
        </Button>
        <div className="text-sm text-neutral-500 whitespace-nowrap">
          {sessions.length}{' '}
          {sessions.length === 1 ? t('Session') : t('sessions')} · {totalMinutes}{' '}
          {t('min')}
        </div>
      </div>

      {expanded
        ? sessions.map((session: any) => (
            <div key={session.sessionId} className="border-t pl-6">
              <SessionItem session={session} {...sessionItemProps(session)} />
            </div>
          ))
        : null}
    </div>
  );
}

function SessionList() {
  const location = useLocation(); // Get the current URL location
  const isBookmark = location.pathname.includes('/bookmarks');
  const { t } = useTranslation();

  const {
    projectsStore,
    sessionStore,
    customFieldStore,
    userStore,
    searchStore,
    filterStore,
  } = useStore();
  const { isEnterprise } = userStore;
  const { isLoggedIn } = userStore;
  const { lastPlayedSessionId, list, total, sessionGroups } = sessionStore;
  const loading = sessionStore.loadingSessions;
  const onToggleFavorite = sessionStore.toggleFavorite;
  const { updateProjectRecordingStatus, siteId, previousSiteid } =
    projectsStore;
  const { currentPage, activeTab, pageSize } = searchStore;
  const { groupByUser } = searchStore.instance;
  const groupedView = groupByUser && !isBookmark;
  const { filters } = searchStore.instance;
  const _filterKeys = filters.map((i: any) => i.key);
  const hasUserFilter =
    _filterKeys.includes(FilterKey.USERID) ||
    _filterKeys.includes(FilterKey.USERANONYMOUSID);
  const isVault = isBookmark && isEnterprise;
  const activeSite = projectsStore.active;
  const hasNoRecordings = !activeSite || !activeSite.recorded;
  const metaList = customFieldStore.list;

  useEffect(() => {
    if (!searchStore.urlParsed || siteId !== previousSiteid) return;
    void searchStore.checkForLatestSessionCount();
  }, [location.pathname]);

  const NO_CONTENT = React.useMemo(() => {
    if (isBookmark && !isEnterprise) {
      return {
        icon: ICONS.NO_BOOKMARKS,
        message: t('No sessions bookmarked'),
      };
    }
    if (isVault) {
      return {
        icon: ICONS.NO_SESSIONS_IN_VAULT,
        message: t('No sessions found in vault'),
      };
    }
    return {
      icon: ICONS.NO_SESSIONS,
      message: <SessionDateRange />,
    };
  }, [isBookmark, isVault, activeTab, location.pathname]);
  const [statusData, setStatusData] = React.useState<SessionStatus>({
    status: 0,
    count: 0,
  });

  const fetchStatus = async () => {
    const response = await sessionService.getRecordingStatus();
    setStatusData({
      status: response.recordingStatus,
      count: response.sessionsCount,
    });
  };

  useEffect(() => {
    if (!hasNoRecordings || !activeSite || !isLoggedIn) {
      return;
    }

    void fetchStatus();

    sessionStatusTimeOut = setInterval(() => {
      void fetchStatus();
    }, STATUS_FREQUENCY);

    return () => clearInterval(sessionStatusTimeOut);
  }, [hasNoRecordings, activeSite, isLoggedIn]);

  useEffect(() => {
    if (!hasNoRecordings && statusData.status === 0) {
      return;
    }

    // recording && processed
    if (statusData.status === 2 && siteId) {
      updateProjectRecordingStatus(siteId, true);
      clearInterval(sessionStatusTimeOut);
    }
  }, [statusData, siteId]);

  useEffect(() => {
    const id = setInterval(() => {
      if (!document.hidden) {
        void searchStore.checkForLatestSessionCount();
      }
    }, AUTO_REFRESH_INTERVAL);
    return () => clearInterval(id);
  }, []);

  useEffect(() => {
    // handle scroll position
    const { scrollY } = searchStore;
    window.scrollTo(0, scrollY);

    return () => {
      searchStore.setScrollPosition(window.scrollY);
    };
  }, []);

  const refreshOnActive = () => {
    if (document.hidden && !!sessionTimeOut) {
      clearTimeout(sessionTimeOut);
      return;
    }

    sessionTimeOut = setTimeout(function () {
      if (!document.hidden) {
        void searchStore.checkForLatestSessionCount();
      }
    }, 5000);
  };

  useEffect(() => {
    document.addEventListener('visibilitychange', refreshOnActive);
    return () => {
      document.removeEventListener('visibilitychange', refreshOnActive);
    };
  }, []);

  const onUserClick = (userId: any) => {
    const userIdFilter = filterStore.findEvent({
      name: FilterKey.USERID,
      category: 'user',
    });
    if (!userIdFilter) {
      return;
    }
    userIdFilter.value = [userId];
    searchStore.addFilter(userIdFilter);
  };

  const toggleFavorite = (sessionId: string) => {
    onToggleFavorite(sessionId).then(() => {
      void searchStore.fetchSessions();
    });
  };

  return (
    <Loader loading={loading}>
      {hasNoRecordings && statusData.status == 1 ? (
        <RecordingStatus data={statusData} />
      ) : (
        <>
          <NoContent
            title={
              <div className="flex items-center justify-center flex-col">
                <span className="py-5">
                  <AnimatedSVG name={NO_CONTENT.icon} size={60} />
                </span>
                <div className="mt-4" />
                <div className="text-center relative text-lg font-medium">
                  {NO_CONTENT.message}
                </div>
              </div>
            }
            subtext={
              <div className="flex flex-col items-center">
                {(isVault || isBookmark) && (
                  <div>
                    {isVault
                      ? t(
                          'Extend the retention period of any session by adding it to your vault directly from the player screen.',
                        )
                      : t(
                          'Effortlessly find important sessions by bookmarking them directly from the player screen.',
                        )}
                  </div>
                )}
                <Button
                  className="mt-4"
                  icon={<Icon name="arrow-repeat" size={20} />}
                  onClick={() => {
                    searchStore.updateCurrentPage(1);
                    void searchStore.fetchSessions(true, isBookmark);
                  }}
                >
                  {t('Refresh')}
                </Button>
              </div>
            }
            show={
              !loading &&
              (groupedView ? sessionGroups.length === 0 : list.length === 0)
            }
          >
            {groupedView
              ? sessionGroups.map((group: any) => (
                  <NearbySessionGroup
                    key={group.groupId}
                    group={group}
                    sessionItemProps={() => ({
                      hasUserFilter,
                      onUserClick,
                      metaList,
                      lastPlayedSessionId,
                      bookmarked: isBookmark,
                      toggleFavorite,
                    })}
                  />
                ))
              : list.map((session: any) => (
                  <div key={session.sessionId} className="border-b">
                    <SessionItem
                      session={session}
                      hasUserFilter={hasUserFilter}
                      onUserClick={onUserClick}
                      metaList={metaList}
                      lastPlayedSessionId={lastPlayedSessionId}
                      bookmarked={isBookmark}
                      toggleFavorite={toggleFavorite}
                    />
                  </div>
                ))}
          </NoContent>

          {total > 0 && (
            <div className="flex items-center justify-between p-5">
              <div>
                {t('Showing')}{' '}
                <span className="font-medium">
                  {(currentPage - 1) * pageSize + 1}
                </span>{' '}
                {t('to')}{' '}
                <span className="font-medium">
                  {(currentPage - 1) * pageSize +
                    (groupedView ? sessionGroups.length : list.length)}
                </span>{' '}
                {t('of')}{' '}
                <span className="font-medium">{numberWithCommas(total)}</span>{' '}
                {groupedView ? t('groups.') : t('sessions.')}
              </div>
              <Pagination
                page={currentPage}
                total={total}
                onPageChange={(page) => searchStore.updateCurrentPage(page)}
                limit={pageSize}
                debounceRequest={1000}
              />
            </div>
          )}
        </>
      )}
    </Loader>
  );
}

export default withRouter(observer(SessionList));

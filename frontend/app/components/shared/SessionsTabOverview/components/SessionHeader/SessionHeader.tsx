import React from 'react';
import { Button, Tooltip } from 'antd';
import { useLocation } from 'App/routing';
import Period from 'Types/app/period';
import SelectDateRange from 'Shared/SelectDateRange';
import { useStore } from 'App/mstore';
import { observer } from 'mobx-react-lite';
import SessionSort from '../SessionSort';
import SessionTags from '../SessionTags';

function SessionHeader() {
  const { searchStore } = useStore();
  const location = useLocation();
  const { startDate, endDate, rangeValue, groupByUser } = searchStore.instance;
  const isBookmarks = location.pathname.includes('/bookmarks');

  const period = Period({
    start: startDate,
    end: endDate,
    rangeName: rangeValue,
  });

  const onDateChange = (e: any) => {
    const dateValues = e.toJSON();
    searchStore.edit(dateValues);
    void searchStore.fetchSessions(true);
  };

  const toggleNearbyGrouping = () => {
    searchStore.edit({
      groupByUser: !groupByUser,
      groupWindowMinutes: 120,
    });
    void searchStore.fetchSessions(true);
  };

  return (
    <div
      className="flex items-center px-4 py-3 justify-between w-full"
      data-test-id="session-list-header"
    >
      <div className={`flex w-full flex-wrap gap-2 justify-between`}>
        <SessionTags />
        <div className={'flex items-center flex-row gap-1'}>
          {!isBookmarks ? (
            <Tooltip title="Group sessions from the same user when the gap between them is at most 2 hours">
              <Button
                size="small"
                type={groupByUser ? 'primary' : 'default'}
                onClick={toggleNearbyGrouping}
              >
                Group nearby · 2h
              </Button>
            </Tooltip>
          ) : null}
          <SelectDateRange
            isAnt
            period={period}
            onChange={onDateChange}
            right
          />
          <SessionSort />
        </div>
      </div>
    </div>
  );
}

export default observer(SessionHeader);

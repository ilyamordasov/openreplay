import { Tooltip } from 'antd';
import { DateTime, Duration } from 'luxon';
import { observer } from 'mobx-react-lite';
import React from 'react';

import { PlayerContext } from 'App/components/Session/playerContext';
import { useStore } from 'App/mstore';

import { getTimelinePosition } from './getTimelinePosition';
import stl from './timeline.module.css';

function formatOffset(ms: number) {
  return Duration.fromMillis(Math.max(ms, 0)).toFormat('mm:ss');
}

function StitchedTimelineMarkers({ scale }: { scale: number }) {
  const { player, store } = React.useContext(PlayerContext);
  const { sessionStore, settingsStore } = useStore();
  const session = sessionStore.current;
  const { clicks = [] } = store.get();

  if (!session.isStitched || !session.stitchedSegments?.length) {
    return null;
  }

  const startedAt = session.startedAt ?? 0;
  const zone =
    session.timezone || settingsStore.sessionSettings.timezone.value || 'local';

  return (
    <>
      {session.stitchedSegments.map((segment, index) => {
        const time = Math.max(segment.targetStartTs - startedAt, 0);
        const left = getTimelinePosition(time, scale);
        const sourceStart = DateTime.fromMillis(segment.sourceStartTs)
          .setZone(zone)
          .toFormat('HH:mm:ss');
        const sourceEnd = DateTime.fromMillis(segment.sourceEndTs)
          .setZone(zone)
          .toFormat('HH:mm:ss');

        return (
          <Tooltip
            key={segment.sessionId}
            title={
              <div className="text-xs">
                <div>
                  Session {index + 1} of {session.stitchedSegments!.length}
                </div>
                <div>Start: {sourceStart}</div>
                <div>End: {sourceEnd}</div>
                <div>Merged: {formatOffset(time)}</div>
                <div className="opacity-70">{segment.sessionId}</div>
              </div>
            }
          >
            <button
              type="button"
              className={stl.stitchedSessionMarker}
              style={{ left: `${left}%` }}
              onClick={(event) => {
                event.stopPropagation();
                player.jump(time);
              }}
              aria-label={`Session ${index + 1} starts at ${sourceStart}`}
            >
              <span className={stl.stitchedSessionLabel}>
                {sourceStart}
              </span>
            </button>
          </Tooltip>
        );
      })}

      {clicks.map((click, index) => {
        const left = getTimelinePosition(click.time, scale);
        const label = click.label || click.selector || 'Click';
        return (
          <Tooltip
            key={`${click.time}-${click.tabId}-${index}`}
            title={
              <div className="text-xs">
                <div>{label}</div>
                <div>Merged: {formatOffset(click.time)}</div>
              </div>
            }
          >
            <button
              type="button"
              className={stl.stitchedClickMarker}
              style={{ left: `${left}%` }}
              onClick={(event) => {
                event.stopPropagation();
                player.jump(click.time);
              }}
              aria-label={`Click at ${formatOffset(click.time)}`}
            />
          </Tooltip>
        );
      })}
    </>
  );
}

export default observer(StitchedTimelineMarkers);

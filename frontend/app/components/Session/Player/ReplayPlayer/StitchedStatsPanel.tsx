import { DateTime } from 'luxon';
import { observer } from 'mobx-react-lite';
import React from 'react';
import { useTranslation } from 'react-i18next';

import { PlayerContext } from 'App/components/Session/playerContext';
import { useStore } from 'App/mstore';

function formatPercent(value: number) {
  return `${Math.round(value * 100)}%`;
}

function StitchedStatsPanel() {
  const { t } = useTranslation();
  const { player } = React.useContext(PlayerContext);
  const { sessionStore, settingsStore } = useStore();
  const session = sessionStore.current;
  const stats = session.stitchedStats;

  if (!session.isStitched || !stats) {
    return null;
  }

  const startedAt = session.startedAt ?? 0;
  const zone =
    session.timezone || settingsStore.sessionSettings.timezone.value || 'local';
  const misclickRate =
    stats.clickCount > 0 ? stats.misclickCount / stats.clickCount : 0;

  return (
    <aside className="w-[320px] min-w-[320px] h-full border-l bg-white overflow-y-auto">
      <div className="p-4 border-b">
        <div className="font-semibold text-base">{t('Group stats')}</div>
        <div className="text-xs text-neutral-500 mt-1">
          {t('Merged replay')} · {stats.sessionCount} {t('sessions')}
        </div>
        {!stats.analyticsComplete ? (
          <div className="mt-2 text-xs text-orange-600">
            {t('Some session analytics could not be loaded')}
          </div>
        ) : null}
      </div>

      <div className="grid grid-cols-2 gap-2 p-4 border-b">
        <div className="rounded border p-3">
          <div className="text-xs text-neutral-500">{t('Sessions')}</div>
          <div className="text-xl font-semibold mt-1">{stats.sessionCount}</div>
        </div>
        <div className="rounded border p-3">
          <div className="text-xs text-neutral-500">{t('Clicks')}</div>
          <div className="text-xl font-semibold mt-1">{stats.clickCount}</div>
        </div>
        <div className="rounded border p-3">
          <div className="text-xs text-neutral-500">{t('Misclicks')}</div>
          <div className="text-xl font-semibold mt-1 text-red-600">
            {stats.misclickCount}
          </div>
        </div>
        <div className="rounded border p-3">
          <div className="text-xs text-neutral-500">{t('Misclick rate')}</div>
          <div className="text-xl font-semibold mt-1">
            {formatPercent(misclickRate)}
          </div>
        </div>
      </div>

      <div className="p-4">
        <div className="font-medium text-sm mb-3">
          {t('Click distribution')}
        </div>
        <div className="space-y-3">
          {stats.sessions.map((item) => {
            const targetOffset = Math.max(item.targetStartTs - startedAt, 0);
            const sourceTime = DateTime.fromMillis(item.sourceStartTs)
              .setZone(zone)
              .toFormat('HH:mm:ss');
            return (
              <button
                type="button"
                key={item.sessionId}
                className="w-full text-left rounded p-2 -mx-2 hover:bg-neutral-50"
                onClick={() => player.jump(targetOffset)}
              >
                <div className="flex items-center justify-between gap-2 text-xs">
                  <span className="font-medium">
                    {t('Session')} {item.index + 1}
                  </span>
                  <span className="text-neutral-500">{sourceTime}</span>
                </div>
                <div className="flex items-center justify-between gap-2 text-xs mt-1">
                  <span>
                    {item.clickCount} {t('clicks')}
                  </span>
                  <span className={item.misclickCount > 0 ? 'text-red-600' : 'text-neutral-500'}>
                    {item.misclickCount} {t('misclicks')}
                  </span>
                </div>
                <div className="h-1.5 rounded bg-neutral-100 mt-2 overflow-hidden">
                  <div
                    className="h-full bg-[#394eff] rounded"
                    style={{ width: formatPercent(item.share) }}
                  />
                </div>
                <div className="text-[11px] text-neutral-400 mt-1">
                  {formatPercent(item.share)}
                </div>
              </button>
            );
          })}
        </div>
      </div>
    </aside>
  );
}

export default observer(StitchedStatsPanel);

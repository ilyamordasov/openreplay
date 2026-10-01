export const usageCode = `import { tracker } from '@openreplay/tracker';

tracker.configure({
  projectKey: "PROJECT_KEY",
  ingestPoint: "https://d5dofp5kb7k6e2bmrfp7.7qsg961h.apigw.yandexcloud.net/ingest",
});
tracker.start()`;
export const usageCodeSST = `import { tracker } from '@openreplay/tracker/cjs';
// alternatively you can use dynamic import without /cjs suffix to prevent issues with window scope

tracker.configure({
  projectKey: "PROJECT_KEY",
  ingestPoint: "https://d5dofp5kb7k6e2bmrfp7.7qsg961h.apigw.yandexcloud.net/ingest",
});

function MyApp() {
  useEffect(() => { // use componentDidMount in case of React Class Component
    tracker.start()
  }, []);

  //...
}`;

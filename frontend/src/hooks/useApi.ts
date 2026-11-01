// useApi — a tiny data-fetching hook that models loading/error/success
// explicitly, so pages can render StateViews instead of guessing.
import { useCallback, useEffect, useState } from "react";
import type { ApiResult } from "../api/client";

type Fetcher<T> = () => Promise<ApiResult<T>>;

export function useApi<T>(fetcher: Fetcher<T>, deps: unknown[]) {
  const [state, setState] = useState<{ status: "idle" } | { status: "loading" } | {
    status: "ready";
    data: T;
  } | { status: "error"; error: string; httpStatus: number }>({ status: "idle" });
  const [nonce, setNonce] = useState(0);

  useEffect(() => {
    let cancelled = false;
    setState({ status: "loading" });
    fetcher().then((res: ApiResult<T>) => {
      if (cancelled) return;
      if (res.ok) {
        setState({ status: "ready", data: res.data });
      } else {
        setState({ status: "error", error: res.error, httpStatus: res.status });
      }
    });
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [nonce, ...deps]);

  const retry = useCallback(() => setNonce((n) => n + 1), []);
  return { state, retry };
}

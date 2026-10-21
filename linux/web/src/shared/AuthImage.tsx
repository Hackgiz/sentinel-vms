// An image from the management server that works with either client: the
// Admin's cookie client can point <img src> at the URL (the browser sends the
// cookie), but a bearer-token client (the Operator) can't put its token on an
// <img>, so the bytes come through the client and show as an object URL.
import { useEffect, useState } from "react";
import type { ApiClient } from "./api";

/** How to load `path`: straight from <img src>, or through the client. Pure, for the tests. */
export function imageSource(client: ApiClient, path: string, direct: boolean): { src: string } | { fetch: string } {
  return direct ? { src: client.baseUrl + path } : { fetch: path };
}

export interface AuthImageProps {
  client: ApiClient;
  /** Path under the server, e.g. /api/v2/faces/<id>/crop.jpg. */
  path: string;
  /** True for a cookie client (plain <img src>). */
  direct: boolean;
  className?: string;
  alt: string;
  width?: number;
  height?: number;
  /** Shown until the bytes arrive (or when they can't be read). */
  placeholder?: React.ReactNode;
}

export default function AuthImage({ client, path, direct, className, alt, width, height, placeholder }: AuthImageProps) {
  const how = imageSource(client, path, direct);
  const fetchPath = "fetch" in how ? how.fetch : "";
  const [url, setUrl] = useState<string | null>(null);
  useEffect(() => {
    setUrl(null);
    if (!fetchPath) return;
    const ac = new AbortController();
    let objectUrl = "";
    client
      .blob(fetchPath, ac.signal)
      .then((b) => {
        objectUrl = URL.createObjectURL(b);
        setUrl(objectUrl);
      })
      .catch(() => {});
    return () => {
      ac.abort();
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    };
  }, [client, fetchPath]);
  const src = "src" in how ? how.src : url;
  if (!src) return <>{placeholder ?? <span className={className} role="img" aria-label={alt} style={width ? { display: "inline-block", width, height } : undefined} />}</>;
  return <img className={className} src={src} alt={alt} loading="lazy" width={width} height={height} />;
}

// WAVE23(J): the small phone mark in a live tile's title bar, for cameras that are iPhones.
import { Icon } from "./Icons";

export function PhoneTileBadge() {
  return (
    <span className="badge phone" title="iPhone camera">
      <Icon name="phone" size={12} label="iPhone camera" />
    </span>
  );
}

// Sentinel Admin → Basics → Sentinel Cloud: the same card as the main
// sidebar's Sentinel Cloud page (WAVE13(C); wave 12's code-and-website page
// lived here). The card is pages/CloudPage.tsx, its logic ../cloudAccount.ts.
import { CloudPanel } from "../pages/CloudPage";

export default function CloudSection({ canEdit }: { canEdit: boolean }) {
  return <CloudPanel canEdit={canEdit} />;
}

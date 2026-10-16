// Notifications: the existing iPhone push and MQTT / Home Assistant settings
// regrouped on one page. They keep their v1 endpoints and test buttons.
import { PushSettings } from "../pages/RulesPage";
import MQTTCard from "../pages/MQTTCard";
import "../pages/rules.css";

export default function NotificationsSection() {
  return (
    <>
      <div className="page-head">
        <div className="grow">
          <h1>Notifications</h1>
          <div className="sub">Where alarms are sent outside the dashboard: iPhone push and MQTT / Home Assistant. Webhooks have their own page.</div>
        </div>
      </div>
      <PushSettings />
      <MQTTCard />
    </>
  );
}

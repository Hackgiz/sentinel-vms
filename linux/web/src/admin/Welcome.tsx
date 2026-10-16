// Welcome pane shown when the site root is selected (or the hash is unknown).
import type { NavTree } from "./navModel";

export default function Welcome({ title, tree }: { title: string; tree: NavTree }) {
  return (
    <section className="admin-welcome" aria-label="Welcome">
      <img className="welcome-mark" src="/favicon.svg" alt="" aria-hidden="true" />
      <h2>{title}</h2>
      <p className="muted">
        Manage your recording servers, cameras, rules and people from one place. Pick a page from the tree on the left, or jump straight into a category below.
      </p>
      <ul className="welcome-links">
        {tree.categories.map((c) => (
          <li key={c.slug}>
            <a href={c.leaves[0].hash}>{c.label}</a>
            <span className="muted"> — {c.leaves.map((l) => l.label).join(", ")}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}

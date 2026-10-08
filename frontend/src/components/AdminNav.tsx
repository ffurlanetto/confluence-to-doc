import { NavLink } from 'react-router-dom';

/** Links between the administration pages, shown on each of them. */
export function AdminNav() {
  return (
    <nav aria-label="Administration" className="subnav">
      <NavLink to="/admin/queue">Queue</NavLink>
      <NavLink to="/admin/users">Users</NavLink>
      <NavLink to="/admin/usage">Usage</NavLink>
      <NavLink to="/admin/template">Word template</NavLink>
      <NavLink to="/admin/audit">Audit</NavLink>
    </nav>
  );
}

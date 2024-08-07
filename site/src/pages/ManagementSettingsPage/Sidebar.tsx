import { cx } from "@emotion/css";
import type { Interpolation, Theme } from "@emotion/react";
import AddIcon from "@mui/icons-material/Add";
import SettingsIcon from "@mui/icons-material/Settings";
import type { FC, ReactNode } from "react";
import { Link, NavLink, useLocation, useParams } from "react-router-dom";
import type { Organization } from "api/typesGenerated";
import { Sidebar as BaseSidebar } from "components/Sidebar/Sidebar";
import { Stack } from "components/Stack/Stack";
import { UserAvatar } from "components/UserAvatar/UserAvatar";
import { type ClassName, useClassName } from "hooks/useClassName";
import { useFeatureVisibility } from "modules/dashboard/useFeatureVisibility";
import { linkToAuditing, linkToUsers, withFilter } from "modules/navigation";
import { useOrganizationSettings } from "./ManagementSettingsLayout";

export const Sidebar: FC = () => {
  const { organizations } = useOrganizationSettings();
  const { organization } = useParams() as { organization?: string };
  const { multiple_organizations: organizationsEnabled } =
    useFeatureVisibility();

  let organizationName = organization;
  if (location.pathname === "/organizations") {
    organizationName = getOrganizationNameByDefault(organizations);
  }

  // TODO: Do something nice to scroll to the active org.

  return (
    <BaseSidebar>
      {organizationsEnabled && (
        <header css={styles.sidebarHeader}>Deployment</header>
      )}
      <DeploymentSettingsNavigation
        organizationsEnabled={organizationsEnabled}
      />
      {organizationsEnabled && (
        <>
          <header css={styles.sidebarHeader}>Organizations</header>
          <SidebarNavItem
            active="auto"
            href="/organizations/new"
            icon={<AddIcon />}
          >
            New organization
          </SidebarNavItem>
          {organizations.map((org) => (
            <OrganizationSettingsNavigation
              key={org.id}
              organization={org}
              active={org.name === organizationName}
            />
          ))}
        </>
      )}
    </BaseSidebar>
  );
};

interface DeploymentSettingsNavigationProps {
  organizationsEnabled?: boolean;
}

const DeploymentSettingsNavigation: FC<DeploymentSettingsNavigationProps> = ({
  organizationsEnabled,
}) => {
  const location = useLocation();
  const active = location.pathname.startsWith("/deployment");

  return (
    <div css={{ paddingBottom: 12 }}>
      <SidebarNavItem
        active={active}
        href="/deployment/general"
        // 24px matches the width of the organization icons, and the component is smart enough
        // to keep the icon itself square. It looks too big if it's 24x24.
        icon={<SettingsIcon css={{ width: 24, height: 20 }} />}
      >
        Deployment
      </SidebarNavItem>
      {active && (
        <Stack spacing={0.5} css={{ marginBottom: 8, marginTop: 8 }}>
          <SidebarNavSubItem href="general">General</SidebarNavSubItem>
          <SidebarNavSubItem href="licenses">Licenses</SidebarNavSubItem>
          <SidebarNavSubItem href="appearance">Appearance</SidebarNavSubItem>
          <SidebarNavSubItem href="userauth">
            User Authentication
          </SidebarNavSubItem>
          <SidebarNavSubItem href="external-auth">
            External Authentication
          </SidebarNavSubItem>
          {/* Not exposing this yet since token exchange is not finished yet.
          <SidebarNavSubItem href="oauth2-provider/ap>
            OAuth2 Applications
          </SidebarNavSubItem>*/}
          <SidebarNavSubItem href="network">Network</SidebarNavSubItem>
          <SidebarNavSubItem href="workspace-proxies">
            Workspace Proxies
          </SidebarNavSubItem>
          <SidebarNavSubItem href="security">Security</SidebarNavSubItem>
          <SidebarNavSubItem href="observability">
            Observability
          </SidebarNavSubItem>
          <SidebarNavSubItem href={linkToUsers.slice(1)}>
            Users
          </SidebarNavSubItem>
          {!organizationsEnabled && (
            <SidebarNavSubItem href="groups">Groups</SidebarNavSubItem>
          )}
          <SidebarNavSubItem href={linkToAuditing.slice(1)}>
            Auditing
          </SidebarNavSubItem>
        </Stack>
      )}
    </div>
  );
};

function urlForSubpage(organizationName: string, subpage: string = ""): string {
  return `/organizations/${organizationName}/${subpage}`;
}

interface OrganizationSettingsNavigationProps {
  organization: Organization;
  active: boolean;
}

export const OrganizationSettingsNavigation: FC<
  OrganizationSettingsNavigationProps
> = ({ organization, active }) => {
  return (
    <>
      <SidebarNavItem
        active={active}
        href={urlForSubpage(organization.name)}
        icon={
          <UserAvatar
            key={organization.id}
            size="sm"
            username={organization.display_name}
            avatarURL={organization.icon}
          />
        }
      >
        {organization.display_name}
      </SidebarNavItem>
      {active && (
        <Stack spacing={0.5} css={{ marginBottom: 8, marginTop: 8 }}>
          <SidebarNavSubItem end href={urlForSubpage(organization.name)}>
            Organization settings
          </SidebarNavSubItem>
          <SidebarNavSubItem href={urlForSubpage(organization.name, "members")}>
            Members
          </SidebarNavSubItem>
          <SidebarNavSubItem href={urlForSubpage(organization.name, "groups")}>
            Groups
          </SidebarNavSubItem>
          {/* For now redirect to the site-wide audit page with the organization
              pre-filled into the filter.  Based on user feedback we might want
              to serve a copy of the audit page or even delete this link. */}
          <SidebarNavSubItem
            href={`/deployment${withFilter(
              linkToAuditing,
              `organization:${organization.name}`,
            )}`}
          >
            Auditing
          </SidebarNavSubItem>
        </Stack>
      )}
    </>
  );
};

interface SidebarNavItemProps {
  active?: boolean | "auto";
  children?: ReactNode;
  icon?: ReactNode;
  href: string;
}

export const SidebarNavItem: FC<SidebarNavItemProps> = ({
  active,
  children,
  href,
  icon,
}) => {
  const link = useClassName(classNames.link, []);
  const activeLink = useClassName(classNames.activeLink, []);

  const content = (
    <Stack alignItems="center" spacing={1.5} direction="row">
      {icon}
      {children}
    </Stack>
  );

  if (active === "auto") {
    return (
      <NavLink
        to={href}
        className={({ isActive }) => cx([link, isActive && activeLink])}
      >
        {content}
      </NavLink>
    );
  }

  return (
    <Link to={href} className={cx([link, active && activeLink])}>
      {content}
    </Link>
  );
};

interface SidebarNavSubItemProps {
  children?: ReactNode;
  href: string;
  end?: boolean;
}

export const SidebarNavSubItem: FC<SidebarNavSubItemProps> = ({
  children,
  href,
  end,
}) => {
  const link = useClassName(classNames.subLink, []);
  const activeLink = useClassName(classNames.activeSubLink, []);

  return (
    <NavLink
      end={end}
      to={href}
      className={({ isActive }) => cx([link, isActive && activeLink])}
    >
      {children}
    </NavLink>
  );
};

const styles = {
  sidebarHeader: {
    textTransform: "uppercase",
    letterSpacing: "0.15em",
    fontSize: 11,
    fontWeight: 500,
    paddingBottom: 4,
  },
} satisfies Record<string, Interpolation<Theme>>;

const classNames = {
  link: (css, theme) => css`
    color: inherit;
    display: block;
    font-size: 14px;
    text-decoration: none;
    padding: 10px 12px 10px 16px;
    border-radius: 4px;
    transition: background-color 0.15s ease-in-out;
    position: relative;

    &:hover {
      background-color: ${theme.palette.action.hover};
    }

    border-left: 3px solid transparent;
  `,

  activeLink: (css, theme) => css`
    border-left-color: ${theme.palette.primary.main};
    border-top-left-radius: 0;
    border-bottom-left-radius: 0;
  `,

  subLink: (css, theme) => css`
    color: inherit;
    text-decoration: none;

    display: block;
    font-size: 13px;
    margin-left: 44px;
    padding: 4px 12px;
    border-radius: 4px;
    transition: background-color 0.15s ease-in-out;
    margin-bottom: 1px;
    position: relative;

    &:hover {
      background-color: ${theme.palette.action.hover};
    }
  `,

  activeSubLink: (css) => css`
    font-weight: 600;
  `,
} satisfies Record<string, ClassName>;

const getOrganizationNameByDefault = (organizations: Organization[]) =>
  organizations.find((org) => org.is_default)?.name;

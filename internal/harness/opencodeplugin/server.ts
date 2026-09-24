const setup = async (api: any) => {
  if (api?.shell && typeof api.shell.hook === "function") {
    api.shell.hook("create.before", (spec: any) => {
      if (spec?.env && spec.env.RELEVO_HARNESS === undefined) {
        spec.env.RELEVO_HARNESS = "opencode";
      }
    });
  }
};

export default {
  id: "relevo-server",
  setup,
};

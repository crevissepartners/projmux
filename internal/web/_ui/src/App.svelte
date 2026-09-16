<script lang="ts">
  let version = $state("");
  $effect(() => {
    fetch("/api/v1/version")
      .then((res) => res.json())
      .then((body: { version: string }) => (version = body.version));
  });
</script>

<main>projmux {version}</main>

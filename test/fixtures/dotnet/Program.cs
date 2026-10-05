// No OpenTelemetry code: spans in Tsuga prove the injected agent attached.
using System.Text.Json;

var builder = WebApplication.CreateBuilder(args);
builder.WebHost.UseUrls("http://0.0.0.0:8080");
builder.Logging.AddFilter("Microsoft.AspNetCore", LogLevel.Warning);
var app = builder.Build();

app.MapGet("/{**path}", (HttpContext context) =>
{
    Console.WriteLine(JsonSerializer.Serialize(new
    {
        level = "info",
        msg = "handled",
        path = context.Request.Path.Value
    }));
    return Results.Text("ok\n");
});

// Self-driving: no external load generator needed.
_ = Task.Run(async () =>
{
    using var client = new HttpClient();
    while (true)
    {
        await Task.Delay(2000);
        try
        {
            await client.GetAsync("http://127.0.0.1:8080/work");
        }
        catch (Exception err)
        {
            Console.WriteLine(JsonSerializer.Serialize(new { level = "warn", msg = err.Message }));
        }
    }
});

Console.WriteLine(JsonSerializer.Serialize(new { level = "info", msg = "listening", port = 8080 }));
app.Run();

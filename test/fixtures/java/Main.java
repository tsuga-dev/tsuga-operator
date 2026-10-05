// No OpenTelemetry code: spans in Tsuga prove the injected agent attached.
import com.sun.net.httpserver.HttpServer;
import java.io.OutputStream;
import java.net.InetSocketAddress;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;

public class Main {
    private static final int PORT = 8080;

    public static void main(String[] args) throws Exception {
        HttpServer server = HttpServer.create(new InetSocketAddress(PORT), 0);
        server.createContext("/", exchange -> {
            System.out.println("{\"level\":\"info\",\"msg\":\"handled\",\"path\":\""
                + exchange.getRequestURI().getPath() + "\"}");
            byte[] body = "ok\n".getBytes();
            exchange.sendResponseHeaders(200, body.length);
            try (OutputStream out = exchange.getResponseBody()) {
                out.write(body);
            }
        });
        server.start();
        System.out.println("{\"level\":\"info\",\"msg\":\"listening\",\"port\":" + PORT + "}");

        HttpClient client = HttpClient.newHttpClient();
        Thread driver = new Thread(() -> {
            while (true) {
                try {
                    Thread.sleep(2000);
                    client.send(
                        HttpRequest.newBuilder(URI.create("http://127.0.0.1:" + PORT + "/work")).build(),
                        HttpResponse.BodyHandlers.discarding());
                } catch (Exception err) {
                    System.out.println("{\"level\":\"warn\",\"msg\":\"" + err.getMessage() + "\"}");
                }
            }
        });
        driver.setDaemon(true);
        driver.start();
    }
}
